
# TaskFlow — API de Tareas en Go (Gin + GORM + Postgres + JWT)

API sencilla pero completa para gestionar tareas por usuario, con autenticación JWT, persistencia en Postgres y un **worker concurrente** (goroutines + channel) que programa recordatorios con `time.AfterFunc`.

> Pensado para practicar Go en backend y enseñar en entrevistas: diseño de API, auth, DB real, y concurrencia.

## Stack
- **Go** (Gin, GORM, JWT, bcrypt)
- **Postgres** (Docker)
- **Docker Compose**
- **Concurrencia en Go**: goroutines + channel para recordatorios

## Características
- Registro y login con **JWT**.
- CRUD de tareas por usuario autenticado.
- Campos de tarea: `title`, `done`, `due_at` (ISO8601).
- **Recordatorios** programados en background cuando llega `due_at` (log en consola).
- **AutoMigrate** al arrancar (crea tablas si no existen).
- Healthcheck `/health`.

---

## Arranque Rápido

### Requisitos
- Docker y Docker Compose

### 1) Clonar y preparar
```bash
git clone <tu-repo>
cd go-todo
```

Asegúrate de tener estos archivos en la raíz:
- `main.go`
- `go.mod`, `go.sum`
- `Dockerfile`
- `docker-compose.yml`

### 2) Levantar servicios
```bash
docker compose up --build
```

Espera a ver en logs:
```
aplicando migraciones...
migraciones listas
listening on :8080
```

### 3) Probar Health
```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

---

## Variables de entorno (por defecto en compose)
- `JWT_SECRET=prod-change-me` (cámbiala en producción)
- `POSTGRES_DSN="host=db user=postgres password=postgres dbname=taskflow port=5432 sslmode=disable TimeZone=UTC"`

---

## Endpoints

### Health
```
GET /health
200 -> {"status":"ok"}
```

### Auth
```
POST /auth/register      { "email": "...", "password": "..." }  -> 201
POST /auth/login         { "email": "...", "password": "..." }  -> 200 { "token": "JWT" }
```

> El token va en: `Authorization: Bearer <JWT>`

### Tasks (requiere JWT)
```
GET    /api/tasks                         -> 200 [ ... ]
POST   /api/tasks      { "title": "...", "due_at": "2025-09-18T16:00:00Z"? } -> 201
PATCH  /api/tasks/:id  { "title"?, "done"?, "due_at"? } -> 200
DELETE /api/tasks/:id                      -> 200 (o 404 si no existe)
```

---

## Ejemplos (PowerShell)

```powershell
# Registro
$email = "dani+" + ([guid]::NewGuid().ToString("N").Substring(0,6)) + "@example.com"
Invoke-RestMethod -Method Post http://localhost:8080/auth/register -ContentType application/json -Body (@{ email=$email; password="secret123" } | ConvertTo-Json)

# Login -> token
$resp = Invoke-RestMethod -Method Post http://localhost:8080/auth/login -ContentType application/json -Body (@{ email=$email; password="secret123" } | ConvertTo-Json)
$token = $resp.token
$Headers = @{ Authorization = "Bearer $($token)" }

# Crear tarea (vence en 1 min)
$due = (Get-Date).AddMinutes(1).ToString("o")
$task = Invoke-RestMethod -Method Post http://localhost:8080/api/tasks -Headers $Headers -ContentType application/json -Body (@{ title="Estudiar Go"; due_at=$due } | ConvertTo-Json)
$task

# Listar
Invoke-RestMethod http://localhost:8080/api/tasks -Headers $Headers

# Marcar como hecha
Invoke-RestMethod -Method Patch ("http://localhost:8080/api/tasks/{0}" -f $task.id) -Headers $Headers -ContentType application/json -Body '{"done":true}'

# Borrar
Invoke-RestMethod -Method Delete ("http://localhost:8080/api/tasks/{0}" -f $task.id) -Headers $Headers
```

## Ejemplos (curl)

```bash
# Registro
curl -X POST http://localhost:8080/auth/register   -H "Content-Type: application/json"   -d '{"email":"dani@example.com","password":"secret123"}'

# Login
TOKEN=$(curl -s -X POST http://localhost:8080/auth/login   -H "Content-Type: application/json"   -d '{"email":"dani@example.com","password":"secret123"}' | jq -r .token)

# Crear tarea
DUE=$(date -u -d "+1 minute" +"%Y-%m-%dT%H:%M:%SZ")
curl -X POST http://localhost:8080/api/tasks   -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json"   -d "{"title":"Tarea curl","due_at":"$DUE"}"

# Listar
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/tasks
```

---

## Arquitectura (resumen)

- **Gin**: router, middlewares, JSON.
- **GORM** + **Postgres**: modelos `User` y `Task`, migraciones con `AutoMigrate`.
- **JWT**: `POST /auth/login` firma un token HS256 (24h).
- **Concurrencia**:
  - `remindersCh := make(chan uint, 100)`
  - Al crear/actualizar tarea con `due_at`, se envía `taskID` al canal.
  - Worker (`startReminderWorker`) recupera la tarea y programa `time.AfterFunc(delay, ...)`.
  - En `due_at` registra un log de recordatorio (extensible a email/webhook).

---

## Troubleshooting

- **`token requerido`**: el header no se está enviando correctamente. Asegúrate de:
  - `Authorization: Bearer <token>` (con B mayúscula)
  - El token no esté vacío y que no hayas cambiado `JWT_SECRET` después de loguear.
- **`credenciales inválidas`**: email/clave incorrectos o usuario no registrado.
- **Migración falla / tablas no existen**:
  - Revisa logs al arranque (debe mostrar “migraciones listas”).
  - Levanta limpio: `docker compose down -v && docker compose up --build`.
  - Verifica `POSTGRES_DSN` en `docker-compose.yml`.
- **`404` al actualizar/borrar**: el `id` probablemente no es tuyo (otra cuenta) o no existe; primero lista y usa el `id` que te devuelve tu `GET /api/tasks`.

---
