<div align="center">

# chatgpt-codex-proxy

*Habla con cuentas de ChatGPT Codex utilizando cualquier cliente de OpenAI o Anthropic.*

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![Docker](https://img.shields.io/badge/Deploy-Compose-2496ED?style=flat&logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![API](https://img.shields.io/badge/API-OpenAI%20%2B%20Anthropic-412991?style=flat&logo=openai&logoColor=white)]()
[![License](https://img.shields.io/badge/License-MIT-yellow?style=flat)](LICENSE)

</div>

---

```bash
cp .env.example .env          # configurar PROXY_API_KEY
docker compose up -d --build  # o: go run ./cmd/api
```

Expone endpoints de OpenAI y Anthropic, traduce cada solicitud al formato privado de `chatgpt.com/backend-api/codex/*` y las enruta a través de tus cuentas de Codex iniciadas de sesión.

La API upstream es privada y no está documentada, por lo que puede cambiar sin previo aviso. Construido para uso local y a pequeña escala.

## Características

- **Dos superficies de API, un backend** — OpenAI Chat Completions, Responses e Imágenes, además de Anthropic Messages.
- **Streaming en todas partes** — SSE, un WebSocket persistente para Responses, o JSON simple.
- **Rotación de cuentas múltiples** — la menos utilizada (least-used), round-robin o sticky, con tiempos de enfriamiento (cooldowns) y conciencia de cuota.
- **Inicio de sesión de dispositivo** — añade una cuenta abriendo una URL. Sin extracción de cookies, sin tokens pegados.
- **Herramientas y salida estructurada** — herramientas personalizadas, `functions` heredadas, `json_schema`, `json_object`.

## Inicio Rápido

Requiere Docker o Go `1.26.x`, y una `PROXY_API_KEY` aleatoria y larga.

```bash
export PROXY_URL=http://localhost:8080
export PROXY_API_KEY=change-me-to-a-long-random-string
```

Añadir una cuenta — inicia un login de dispositivo, abre la `auth_url` devuelta, luego haz polling hasta que el `status` sea `ready`:

```bash
curl -sS -X POST "${PROXY_URL}/admin/accounts/device-login/start" \
  -H "Authorization: Bearer ${PROXY_API_KEY}"

curl -sS "${PROXY_URL}/admin/accounts/device-login/<login_id>" \
  -H "Authorization: Bearer ${PROXY_API_KEY}"
```

Luego:

```bash
curl -sS "${PROXY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${PROXY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}'
```

## Clientes

Apunta cualquier cliente de OpenAI a `http://localhost:8080/v1` utilizando `PROXY_API_KEY` como la clave de API. Para Claude Code u otro cliente de Anthropic:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_API_KEY="${PROXY_API_KEY}"
export ANTHROPIC_MODEL=gpt-5.6-sol
```

Utiliza un ID de modelo de `GET /v1/models` — no existen alias `claude-*`.

Todas las rutas excepto `GET /health/live` requieren la clave, ya sea como `Authorization: Bearer <key>` o `X-API-Key: <key>`.

## API

```
POST /v1/completions              POST /v1/messages
POST /v1/chat/completions         POST /v1/messages/count_tokens
POST /v1/responses                GET  /v1/models
GET  /v1/responses   (WebSocket)  GET  /v1/models/:model_id
POST /v1/responses/compact        GET  /health
POST /v1/images/generations       GET  /health/live
POST /v1/images/edits
```

Entradas de texto, imagen y archivo, razonamiento, búsqueda web alojada, streaming y no-streaming. El catálogo de modelos proviene del upstream en tiempo de ejecución.

Notas importantes:

- `GET /v1/responses` es un WebSocket, no SSE. El primer mensaje debe ser `response.create`; los turnos posteriores usan `response.create` o `response.append`. No hay marcador `[DONE]`.
- `/v1/chat/completions` también acepta un cuerpo con formato de Responses cuando se omite `messages`.
- Las imágenes usan por defecto `gpt-image-2`. Si el endpoint nativo devuelve `404`, `405` o `501`, recurre a la herramienta de imágenes de Responses — una imagen, data URL.
- Las solicitudes de Anthropic requieren `Anthropic-Version: 2023-06-01`. Los controles de muestreo, secuencias de parada, truncamiento de `max_tokens` y presupuestos de pensamiento son aceptados pero consultivos; Codex no tiene un equivalente.
- La entrada de audio es rechazada. Los cuerpos de solicitud deben ser identity o zstd.

Reglas exactas: [docs/TRANSLATION.md](docs/TRANSLATION.md), [docs/ANTHROPIC.md](docs/ANTHROPIC.md).

## Cuentas

```
GET    /admin/accounts
POST   /admin/accounts/device-login/start
GET    /admin/accounts/device-login/:login_id
DELETE /admin/accounts/:account_id
PATCH  /admin/accounts/:account_id
GET    /admin/accounts/:account_id/usage
POST   /admin/accounts/:account_id/refresh
GET    /admin/rotation
PUT    /admin/rotation
```

La rotación puede ser `least_used`, `round_robin` o `sticky`. Una cuenta se omite cuando su estado es `disabled`, `expired` o `banned`, cuando hay un cooldown activo, falta su token o se ha agotado su cuota. `code_review_rate_limit` se rastrea pero no afecta al enrutamiento.

Un refresco de OAuth fallido solo expira una cuenta en caso de `invalid_grant`. Cualquier otra cosa la mantiene activa tras un cooldown de 60 segundos.

Detalles: [docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md](docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md).

## Despliegue

`compose.yaml` persiste el estado en el volumen `chatgpt-codex-proxy-data` y se ejecuta con un endurecimiento básico. `Dockerfile` construye únicamente el servidor de la API.

```bash
docker compose up -d --build
docker compose logs -f
```

La configuración es solo mediante variables de entorno: `PROXY_API_KEY` (requerido), `PORT` (`8080`), `DATA_DIR` (`data`, o `/app/data` en Docker), `DEBUG_LOG_PAYLOADS` (`false`).

`${DATA_DIR}` contiene `accounts.json` — cuentas, tokens de OAuth, etiquetas, estado, cuota, cooldowns — y `models-cache.json`. El estado de continuación y los logins de dispositivo en curso están solo en memoria y no sobreviven a un reinicio.

## Cómo Funciona

1. Gin acepta una solicitud con formato OpenAI o Anthropic.
2. Los adaptadores la normalizan en un modelo de turno interno.
3. El proxy elige una cuenta lista, traduce y llama a Codex vía SSE o WebSocket.
4. Los eventos del upstream se convierten de nuevo al protocolo que solicitó el cliente.

<img width="4599" height="2073" alt="chatgpt-codex-proxy architecture flowchart" src="https://github.com/user-attachments/assets/05cd8446-dd4b-43bc-a3fc-eb370ad917e6" />

```
POST https://chatgpt.com/backend-api/codex/responses
POST https://chatgpt.com/backend-api/codex/responses/compact
GET  https://chatgpt.com/backend-api/codex/usage
GET  https://chatgpt.com/backend-api/codex/models
WSS  https://chatgpt.com/backend-api/codex/responses
     https://auth.openai.com/api/accounts/deviceauth/*
     https://auth.openai.com/oauth/token
```

## Estructura

```
chatgpt-codex-proxy/
├── cmd/api/                  # punto de entrada del servidor
├── internal/
│   ├── server/               # enrutamiento y handlers de Gin
│   ├── openai/ anthropic/    # adaptadores de protocolo público
│   ├── turn/ translate/      # modelo de turno interno y traducción
│   ├── codex/ codexauth/     # cliente upstream privado y OAuth
│   ├── accounts/             # almacén de cuentas
│   ├── accountmanager/       # rotación, cooldowns, enrutamiento de cuotas
│   ├── devicelogin/          # onboarding de device-auth
│   ├── conversation/         # estado de continuación y afinidad
│   ├── models/               # catálogo de modelos en tiempo de ejecución
│   ├── admin/ middleware/    # API de administración y autenticación
│   └── store/ config/        # persistencia y configuración
├── test/integration/         # suite de compatibilidad en vivo
└── docs/                     # referencias de upstream y traducción
```

## Desarrollo

```bash
go test ./...
```

Pruebas en vivo, contra un proxy que ya tengas ejecutándose:

```bash
OPENAI_API_KEY=change-me-to-a-long-random-string \
OPENAI_MODEL=gpt-5.6-sol \
OPENAI_BASE_URL="${PROXY_URL}/v1" \
go test -tags=live ./test/integration -v -count=1
```

## Documentación

- [docs/TRANSLATION.md](docs/TRANSLATION.md) — Reglas de traducción y compatibilidad de OpenAI a Codex
- [docs/ANTHROPIC.md](docs/ANTHROPIC.md) — Mapeo de Anthropic, streaming, conteo de tokens, límites
- [docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md](docs/MULTI_ACCOUNT_ROTATION_STRATEGY.md) — selección de cuentas y enrutamiento de cuotas
- [docs/CODEX_API_DOCS.md](docs/CODEX_API_DOCS.md) — comportamiento privado del upstream, inferido de este código base

## Limitaciones

- El upstream es privado y puede cambiar sin previo aviso.
- La autenticación de dispositivo es el único flujo de alta (onboarding).
- El estado de continuación está en memoria y expira con su TTL.
- Deliberadamente pequeño; no intenta cubrir cada detalle extremo de la plataforma pública de OpenAI.
