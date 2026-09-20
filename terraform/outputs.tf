output "bucket_name" {
  description = "Nombre del bucket de recibos."
  value       = aws_s3_bucket.receipts.bucket
}

output "bucket_arn" {
  description = "ARN del bucket de recibos."
  value       = aws_s3_bucket.receipts.arn
}
