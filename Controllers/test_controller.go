package controllers

import (
    "database/sql"
    "encoding/json"
    "net/http"
    "strconv"
    
    "backend/Models"
    _ "github.com/lib/pq"
)

type TestController struct {
    DB *sql.DB
}

func NewTestController(db *sql.DB) *TestController {
    return &TestController{DB: db}
}

func (tc *TestController) setSearchPath() error {
    // Set search_path to public schema (required because isolated role has restricted search_path)
    // Using string concatenation to avoid C# string interpolation issues
    _, err := tc.DB.Exec(`SET search_path = public, "$` + `user"`)
    return err
}

func (tc *TestController) GetAll(w http.ResponseWriter, r *http.Request) {
    if err := tc.setSearchPath(); err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    rows, err := tc.DB.Query(`SELECT "Id", "Name" FROM "TestProjects" ORDER BY "Id"`)
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    defer rows.Close()
    
    var projects []models.TestProjects
    for rows.Next() {
        var project models.TestProjects
        if err := rows.Scan(&project.Id, &project.Name); err != nil {
            http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
            return
        }
        projects = append(projects, project)
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(projects)
}

func (tc *TestController) GetById(w http.ResponseWriter, r *http.Request, id int) {
    if err := tc.setSearchPath(); err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    var project models.TestProjects
    err := tc.DB.QueryRow(`SELECT "Id", "Name" FROM "TestProjects" WHERE "Id" = $1`, id).
        Scan(&project.Id, &project.Name)

    if err == sql.ErrNoRows {
        http.Error(w, "Project not found", http.StatusNotFound)
        return
    }
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(project)
}

func (tc *TestController) Create(w http.ResponseWriter, r *http.Request) {
    var project models.TestProjects
    if err := json.NewDecoder(r.Body).Decode(&project); err != nil {
        http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
        return
    }
    
    if err := tc.setSearchPath(); err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    err := tc.DB.QueryRow(
        `INSERT INTO "TestProjects" ("Name") VALUES ($1) RETURNING "Id", "Name"`,
        project.Name,
    ).Scan(&project.Id, &project.Name)

    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(project)
}

func (tc *TestController) Update(w http.ResponseWriter, r *http.Request, id int) {
    var project models.TestProjects
    if err := json.NewDecoder(r.Body).Decode(&project); err != nil {
        http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
        return
    }
    
    if err := tc.setSearchPath(); err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    result, err := tc.DB.Exec(
        `UPDATE "TestProjects" SET "Name" = $1 WHERE "Id" = $2`,
        project.Name, id,
    )
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    rowsAffected, err := result.RowsAffected()
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    if rowsAffected == 0 {
        http.Error(w, "Project not found", http.StatusNotFound)
        return
    }
    
    project.Id = id
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(project)
}

func (tc *TestController) Delete(w http.ResponseWriter, r *http.Request, id int) {
    if err := tc.setSearchPath(); err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    result, err := tc.DB.Exec(`DELETE FROM "TestProjects" WHERE "Id" = $1`, id)
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    rowsAffected, err := result.RowsAffected()
    if err != nil {
        http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    if rowsAffected == 0 {
        http.Error(w, "Project not found", http.StatusNotFound)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"message": "Deleted successfully"})
}

func ExtractId(path string) (int, error) {
    // Extract ID from path like /api/test/123
    idStr := path[len("/api/test/"):]
    return strconv.Atoi(idStr)
}
