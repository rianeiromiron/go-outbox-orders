variable "localstack_endpoint" {
  description = "URL de S3 en LocalStack. Desde el host es el puerto publicado por docker-compose.yml."
  type        = string
  default     = "http://localhost:4567"
}

variable "region" {
  description = "Región de AWS (LocalStack acepta cualquiera)."
  type        = string
  default     = "us-east-1"
}

variable "bucket_name" {
  description = "Nombre del bucket de recibos de notificación."
  type        = string
  default     = "outbox-receipts"

  # Reglas de nombres de S3: 3-63 caracteres, minúsculas, números, puntos y guiones,
  # empezando y terminando con letra o número.
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.bucket_name))
    error_message = "bucket_name debe tener 3-63 caracteres: minúsculas, números, puntos o guiones, y empezar y terminar con letra o número."
  }
}

variable "retention_days" {
  description = "Días que se conservan los recibos antes de expirar."
  type        = number
  default     = 90

  validation {
    condition     = var.retention_days >= 1
    error_message = "retention_days debe ser al menos 1."
  }
}
