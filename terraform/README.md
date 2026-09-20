# terraform — infraestructura simulada (LocalStack)

Infraestructura como código con [Terraform](https://www.terraform.io/) contra un
**LocalStack** (AWS simulado) que corre en el `docker-compose.yml`, dentro de la
red `outbox-net`. **No toca ninguna cuenta real de AWS.**

**Estado:** Fase 6 completada en local; primera ejecución del CI en GitHub
pendiente de confirmar.

## Qué provisiona

Un bucket S3 `outbox-receipts`, pensado para guardar el recibo de cada
notificación (esa integración en el worker **no está implementada**: hoy solo
se provisiona la infraestructura):

| Recurso | Configuración |
|---|---|
| `aws_s3_bucket` | `outbox-receipts`, etiquetas `project`/`environment`/`managed-by`, `force_destroy = true` (**solo local**) |
| `aws_s3_bucket_public_access_block` | los 4 bloqueos activados |
| `aws_s3_bucket_versioning` | `Enabled` |
| `aws_s3_bucket_server_side_encryption_configuration` | `AES256` |
| `aws_s3_bucket_lifecycle_configuration` | los recibos expiran a los 90 días (`retention_days`), las versiones antiguas a los 30 y las subidas multiparte incompletas a los 7 |

Variables (`variables.tf`): `localstack_endpoint` (por defecto
`http://localhost:4567`), `region`, `bucket_name` y `retention_days`, las dos
últimas con validación.

## Cómo usarlo

Requisitos: Docker y Terraform ≥ 1.6. Desde la raíz del repositorio:

```bash
make tf-test      # tests de la configuración (provider simulado; no necesita LocalStack)
make tf-plan      # levanta LocalStack si hace falta y planifica
make tf-apply     # crea el bucket
make tf-destroy   # lo elimina
```

Sin `make`: `docker compose up -d --wait localstack` y luego `terraform init`,
`plan` y `apply` dentro de `terraform/`.

Para comprobar el resultado **sin pasar por Terraform**, pregúntale a LocalStack
(la imagen incluye `awslocal`):

```bash
docker compose exec localstack awslocal s3 ls
docker compose exec localstack awslocal s3api get-bucket-versioning --bucket outbox-receipts
docker compose exec localstack awslocal s3api get-public-access-block --bucket outbox-receipts
```

## LocalStack: versión fijada y por qué

El servicio usa **`localstack/localstack:4.14.0`** (febrero de 2026), la última
versión que arranca sin cuenta ni token. Desde `2026.03` (versionado por
calendario) la imagen termina con `License activation failed` (código 55) si no
hay `LOCALSTACK_AUTH_TOKEN`; se comprobó con `2026.8.3`. No la actualices sin
decidir antes cómo gestionar ese token. El servicio publica el puerto `4567` en
el host (el `4566` habitual lo usa otro proyecto) y su estado va en `tmpfs`:
es efímero a propósito, Terraform lo recrea.

## Estado de Terraform

El estado es **local** (`terraform.tfstate`, ignorado por git). Como el estado de
LocalStack se pierde al apagarlo, tras un `docker compose down` el estado de
Terraform queda desfasado: el siguiente `plan` detecta que el bucket ya no existe
y lo vuelve a crear. En un entorno real el estado iría en un backend remoto con
bloqueo (por ejemplo S3 + DynamoDB).

`.terraform.lock.hcl` **sí se versiona** y fija el provider (`hashicorp/aws`
6.65.0) con hashes para Windows, Linux y macOS (`terraform providers lock`).

## Verificado

Contra un LocalStack real (y los mismos pasos corren en el CI):

| Prueba | Resultado |
|---|---|
| `apply` | 5 recursos creados; `awslocal` confirma versionado, cifrado, bloqueo público, ciclo de vida y etiquetas |
| Segundo `plan` | sin cambios (código 0): es idempotente |
| Uso real | un recibo subido queda cifrado (`AES256`), versionado (2 versiones tras sobrescribir) y con `Expiration` a 90 días |
| **Deriva** | tras quitar a mano el bloqueo público y suspender el versionado, `plan` propone exactamente `1 to add, 1 to change` y `apply` lo corrige |
| `destroy` | elimina los 5 recursos aunque el bucket tenga objetos (`force_destroy`) |
| `terraform test` | 6 tests con provider simulado; se comprobó con mutaciones (desactivar un bloqueo, suspender el versionado, cambiar el cifrado, ignorar `retention_days`, debilitar la validación del nombre) que los tests fallan |

## Límites conocidos

- **LocalStack community no hace cumplir el bloqueo de acceso público.** El
  bloqueo queda configurado (se verifica), pero el emulador aceptó un
  `put-object-acl --acl public-read` que en AWS real habría fallado. Por eso
  aquí solo se puede comprobar que la configuración es la correcta, no que
  proteja.
- **`apply` tarda ~1 minuto en la regla de ciclo de vida:** el provider de AWS
  espera a que esa configuración sea legible y LocalStack responde lento.
- `force_destroy = true` es solo para entorno local; en uno real no debe usarse.
- Las credenciales `test`/`test` del provider son la convención de LocalStack, no
  secretos, y no sirven en AWS.
- Los tests (`tests/receipts.tftest.hcl`) comprueban lo **planeado**, no que un
  proveedor real lo aplique.
