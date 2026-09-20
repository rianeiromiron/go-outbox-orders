# Tests de la configuración. Usan un provider simulado: no necesitan LocalStack ni
# red, así que corren en cualquier sitio (`terraform test`).
#
# Comprueban lo PLANEADO. Que LocalStack o AWS lo apliquen es otra cosa: ver el
# README de terraform/ (p. ej. LocalStack community no hace cumplir el bloqueo de
# acceso público).

mock_provider "aws" {}

run "bucket_is_locked_down_by_default" {
  command = plan

  assert {
    condition     = aws_s3_bucket.receipts.bucket == "outbox-receipts"
    error_message = "el nombre por defecto del bucket debe ser outbox-receipts"
  }

  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.receipts.block_public_acls,
      aws_s3_bucket_public_access_block.receipts.block_public_policy,
      aws_s3_bucket_public_access_block.receipts.ignore_public_acls,
      aws_s3_bucket_public_access_block.receipts.restrict_public_buckets,
    ])
    error_message = "los cuatro bloqueos de acceso público deben estar activos"
  }

  assert {
    condition     = aws_s3_bucket_versioning.receipts.versioning_configuration[0].status == "Enabled"
    error_message = "el versionado debe estar activo"
  }

  assert {
    condition     = one(aws_s3_bucket_server_side_encryption_configuration.receipts.rule).apply_server_side_encryption_by_default[0].sse_algorithm == "AES256"
    error_message = "el cifrado en reposo debe ser AES256"
  }

  assert {
    condition     = aws_s3_bucket.receipts.tags["managed-by"] == "terraform" && aws_s3_bucket.receipts.tags["project"] == "go-outbox-orders"
    error_message = "el bucket debe llevar las etiquetas project y managed-by"
  }
}

run "lifecycle_expires_receipts_and_cleans_up" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.receipts.rule).expiration[0].days == 90
    error_message = "los recibos deben expirar a los 90 días por defecto"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.receipts.rule).noncurrent_version_expiration[0].noncurrent_days == 30
    error_message = "las versiones antiguas deben expirar a los 30 días"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.receipts.rule).abort_incomplete_multipart_upload[0].days_after_initiation == 7
    error_message = "las subidas multiparte incompletas deben abortarse a los 7 días"
  }
}

run "retention_is_configurable" {
  command = plan

  variables {
    retention_days = 7
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.receipts.rule).expiration[0].days == 7
    error_message = "retention_days debe controlar la expiración"
  }
}

run "rejects_invalid_bucket_names" {
  command = plan

  variables {
    bucket_name = "Nombre_Invalido"
  }

  expect_failures = [var.bucket_name]
}

run "rejects_too_short_bucket_name" {
  command = plan

  variables {
    bucket_name = "ab"
  }

  expect_failures = [var.bucket_name]
}

run "rejects_zero_retention" {
  command = plan

  variables {
    retention_days = 0
  }

  expect_failures = [var.retention_days]
}
