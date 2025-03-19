variable "rulefile" {
  type = string
}

lint {
  naming {
    table {
      match = "^[a-z_]+$"
    }
  }
  rule "hcl" "by-var" {
    src = [var.rulefile]
  }
}