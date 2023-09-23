env "test" {
  url = "sqlite://file?mode=memory"
  migration  {
	dir = "file://testdata/migrations"
  }
}