output "api_url" {
  description = "Public HTTPS endpoint for the API"
  value       = aws_apigatewayv2_stage.default.invoke_url
}

output "lambda_function_name" {
  value = aws_lambda_function.api.function_name
}

output "s3_bucket_name" {
  value = aws_s3_bucket.data.bucket
}

output "dynamodb_table_name" {
  value = aws_dynamodb_table.main.name
}

output "api_key" {
  description = "API key for the x-api-key header (terraform output -raw api_key)"
  value       = random_password.api_key.result
  sensitive   = true
}
