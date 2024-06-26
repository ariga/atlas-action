test "schema" "expected_success" {
  exec {
    sql = "select * from t1"
    output = ""
  }
}

test "schema" "expected_failure" {
  exec {
    sql = "select * from t1"
    output = "0"
  }
}