variable "aws_region" {
  description = "AWS region to deploy into"
  type        = string
  default     = "us-east-1"
}

variable "project_name" {
  description = "Prefix used for all resource names"
  type        = string
  default     = "cp-mcp"
}

variable "environment" {
  description = "Deployment environment (dev, staging, prod)"
  type        = string
  default     = "dev"
}

variable "lambda_runtime" {
  description = "Lambda runtime (Go uses the OS-only custom runtime)"
  type        = string
  default     = "provided.al2023"
}

variable "lambda_memory_mb" {
  description = "Lambda memory size in MB"
  type        = number
  default     = 256
}

variable "lambda_timeout_seconds" {
  description = "Lambda timeout in seconds (API Gateway caps requests at 30s)"
  type        = number
  default     = 15
}

variable "cors_allow_origins" {
  description = "Origins allowed to call the API from a browser"
  type        = list(string)
  default     = ["*"]
}

variable "throttle_rate_limit" {
  description = "Steady-state requests per second allowed on the API"
  type        = number
  default     = 50
}

variable "throttle_burst_limit" {
  description = "Burst requests allowed on the API"
  type        = number
  default     = 100
}

variable "log_retention_days" {
  description = "CloudWatch log retention"
  type        = number
  default     = 14
}
