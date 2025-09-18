package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Email        string    `gorm:"uniqueIndex;not null" json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Task struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"index;not null" json:"user_id"`
	Title     string     `gorm:"not null" json:"title"`
	Done      bool       `json:"done"`
	DueAt     *time.Time `json:"due_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

var (
	jwtSecret   = []byte(getEnv("JWT_SECRET", "dev-secret-change-me"))
	remindersCh chan uint
)

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	// --- DB ---
	dsn := getEnv("POSTGRES_DSN", "host=localhost user=postgres password=postgres dbname=taskflow port=5432 sslmode=disable TimeZone=UTC")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("no puedo abrir Postgres:", err)
	}

	// Migraciones (forzamos y verificamos)
	log.Println("aplicando migraciones...")
	if err := db.AutoMigrate(&User{}, &Task{}); err != nil {
		log.Fatal("no puedo migrar:", err)
	}
	if !db.Migrator().HasTable(&User{}) || !db.Migrator().HasTable(&Task{}) {
		log.Fatal("migración NO creó tablas users/tasks (revisar DSN o permisos)")
	}
	log.Println("migraciones listas")

	// --- worker de recordatorios ---
	remindersCh = make(chan uint, 100)
	go startReminderWorker(db, remindersCh)

	// --- server ---
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Auth
	auth := r.Group("/auth")
	{
		auth.POST("/register", registerHandler(db))
		auth.POST("/login", loginHandler(db))
	}

	// API protegida
	api := r.Group("/api")
	api.Use(AuthMiddleware())
	{
		api.GET("/tasks", listTasksHandler(db))
		api.POST("/tasks", createTaskHandler(db))
		api.PATCH("/tasks/:id", updateTaskHandler(db))
		api.DELETE("/tasks/:id", deleteTaskHandler(db))
	}

	log.Println("listening on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}

// ========= AUTH =========

func registerHandler(db *gorm.DB) gin.HandlerFunc {
	type inT struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=6"`
	}
	return func(c *gin.Context) {
		var in inT
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u := User{Email: strings.ToLower(in.Email), PasswordHash: string(hash)}
		if err := db.Create(&u).Error; err != nil {
			c.JSON(409, gin.H{"error": "email ya registrado"})
			return
		}
		c.JSON(201, gin.H{"id": u.ID, "email": u.Email})
	}
}

func loginHandler(db *gorm.DB) gin.HandlerFunc {
	type inT struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required"`
	}
	return func(c *gin.Context) {
		var in inT
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		var u User
		if err := db.Where("email = ?", strings.ToLower(in.Email)).First(&u).Error; err != nil {
			c.JSON(401, gin.H{"error": "credenciales inválidas"})
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
			c.JSON(401, gin.H{"error": "credenciales inválidas"})
			return
		}
		// JWT 24h
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": u.ID,
			"exp": time.Now().Add(24 * time.Hour).Unix(),
			"iat": time.Now().Unix(),
		})
		tokStr, err := token.SignedString(jwtSecret)
		if err != nil {
			c.JSON(500, gin.H{"error": "no se pudo firmar token"})
			return
		}
		c.JSON(200, gin.H{"token": tokStr})
	}
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(401, gin.H{"error": "token requerido"})
			return
		}
		tok := strings.TrimPrefix(h, "Bearer ")
		parsed, err := jwt.Parse(tok, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("alg inválido")
			}
			return jwtSecret, nil
		})
		if err != nil || !parsed.Valid {
			c.AbortWithStatusJSON(401, gin.H{"error": "token inválido"})
			return
		}
		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"error": "claims inválidos"})
			return
		}
		uidF := claims["sub"]
		uid, ok := toUint(uidF)
		if !ok {
			c.AbortWithStatusJSON(401, gin.H{"error": "sub inválido"})
			return
		}
		c.Set("user_id", uint(uid))
		c.Next()
	}
}

func toUint(v any) (uint64, bool) {
	switch t := v.(type) {
	case float64:
		return uint64(t), true
	case int64:
		return uint64(t), true
	case int:
		return uint64(t), true
	case string:
		return 0, false
	default:
		return 0, false
	}
}

// ========= TASKS =========

func listTasksHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.GetUint("user_id")
		var tasks []Task
		if err := db.Where("user_id = ?", uid).Order("id desc").Find(&tasks).Error; err != nil {
			c.JSON(500, gin.H{"error": "db error"})
			return
		}
		c.JSON(200, tasks)
	}
}

func createTaskHandler(db *gorm.DB) gin.HandlerFunc {
	type inT struct {
		Title string  `json:"title" binding:"required"`
		DueAt *string `json:"due_at"` 
	}
	return func(c *gin.Context) {
		uid := c.GetUint("user_id")
		var in inT
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		var due *time.Time
		if in.DueAt != nil && *in.DueAt != "" {
			if t, err := time.Parse(time.RFC3339, *in.DueAt); err == nil {
				due = &t
			}
		}
		t := Task{UserID: uid, Title: in.Title, DueAt: due}
		if err := db.Create(&t).Error; err != nil {
			c.JSON(500, gin.H{"error": "db error"})
			return
		}
		if t.DueAt != nil {
			remindersCh <- t.ID
		}
		c.JSON(201, t)
	}
}

func updateTaskHandler(db *gorm.DB) gin.HandlerFunc {
	type inT struct {
		Title *string `json:"title"`
		Done  *bool   `json:"done"`
		DueAt *string `json:"due_at"`
	}
	return func(c *gin.Context) {
		uid := c.GetUint("user_id")
		var t Task
		if err := db.Where("user_id = ? AND id = ?", uid, c.Param("id")).First(&t).Error; err != nil {
			c.JSON(404, gin.H{"error": "task no encontrada"})
			return
		}	
		var in inT
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if in.Title != nil {
			t.Title = *in.Title
		}
		if in.Done != nil {
			t.Done = *in.Done
		}
		if in.DueAt != nil {
			if *in.DueAt == "" {
				t.DueAt = nil
			} else if parsed, err := time.Parse(time.RFC3339, *in.DueAt); err == nil {
				t.DueAt = &parsed
			}
		}
		if err := db.Save(&t).Error; err != nil {
			c.JSON(500, gin.H{"error": "db error"})
			return
		}
		if t.DueAt != nil && !t.Done {
			remindersCh <- t.ID
		}
		c.JSON(200, t)
	}
}

func deleteTaskHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.GetUint("user_id")
		res := db.Where("user_id = ? AND id = ?", uid, c.Param("id")).Delete(&Task{})
		if res.Error != nil {
			c.JSON(500, gin.H{"error": "db error"})
			return
		}
		if res.RowsAffected == 0 {
			c.JSON(404, gin.H{"error": "task no encontrada"})
			return
		}
		c.JSON(200, gin.H{"deleted": c.Param("id")})
	}
}


// ========= REMINDERS =========

func startReminderWorker(db *gorm.DB, ch <-chan uint) {
	for id := range ch {
		go func(taskID uint) {
			var t Task
			if err := db.First(&t, taskID).Error; err != nil {
				return
			}
			if t.DueAt == nil || t.Done {
				return
			}
			delay := time.Until(*t.DueAt)
			if delay < 0 {
				delay = 0
			}
			time.AfterFunc(delay, func() {
				log.Printf("[REMINDER] Task #%d (user %d): %q vence ahora", t.ID, t.UserID, t.Title)
			})
		}(id)
	}
}
