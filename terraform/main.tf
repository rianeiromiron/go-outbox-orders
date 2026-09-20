# Provider de AWS apuntando a LocalStack (no toca ninguna cuenta real de AWS).
#
# Las credenciales "test"/"test" son la convención de LocalStack, no secretos: no
# sirven en AWS. Las omisiones (skip_*) evitan llamadas a STS/IMDS que LocalStack
# no necesita, y s3_use_path_style es obligatorio porque el host es "localhost"
# (con el estilo virtual-hosted el bucket iría en el nombre DNS).
provider "aws" {
  region                      = var.region
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    s3 = var.localstack_endpoint
  }
}

locals {
  tags = {
    project     = "go-outbox-orders"
    environment = "local"
    managed-by  = "terraform"
  }
}

# Bucket donde, en una extensión futura, el worker guardaría el recibo de cada
# notificación. Hoy solo se provisiona la infraestructura.
resource "aws_s3_bucket" "receipts" {
  bucket = var.bucket_name

  # Solo entorno local: permite `terraform destroy` aunque el bucket tenga objetos.
  # En un entorno real NO se debe activar.
  force_destroy = true

  tags = local.tags
}

# Los recibos no deben ser públicos bajo ninguna circunstancia.
resource "aws_s3_bucket_public_access_block" "receipts" {
  bucket = aws_s3_bucket.receipts.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "receipts" {
  bucket = aws_s3_bucket.receipts.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "receipts" {
  bucket = aws_s3_bucket.receipts.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Los recibos expiran solos; las versiones antiguas y las subidas multiparte
# incompletas no se acumulan indefinidamente.
resource "aws_s3_bucket_lifecycle_configuration" "receipts" {
  bucket = aws_s3_bucket.receipts.id

  # El ciclo de vida de versiones antiguas solo tiene sentido con versionado activo.
  depends_on = [aws_s3_bucket_versioning.receipts]

  rule {
    id     = "expire-receipts"
    status = "Enabled"

    filter {}

    expiration {
      days = var.retention_days
    }

    noncurrent_version_expiration {
      noncurrent_days = 30
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}
