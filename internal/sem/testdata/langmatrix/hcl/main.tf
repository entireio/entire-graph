variable "region" {
  type    = string
  default = "us-east-1"
}

resource "aws_s3_bucket" "ledger" {
  bucket = "ledger-${var.region}"
}

output "ledger_bucket" {
  value = aws_s3_bucket.ledger.bucket
}
