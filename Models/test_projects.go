package models

type TestProjects struct {
    Id   int    `json:"Id" db:"Id"`
    Name string `json:"Name" db:"Name"`
}
