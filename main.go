package main

import (
    "database/sql"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "runtime"
    "strconv"
    "strings"
    "time"

    "backend/Controllers"
    _ "github.com/lib/pq"
)

// Configure logging - Warning and Error only
// Create a custom logger that only shows warnings and errors
func init() {
    // Set log flags to include timestamp
    log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
    // Note: Go's standard log package doesn't have severity levels,
    // but we can use log.Printf for warnings and log.Fatal/panic for errors
    // For production, consider using logrus or zap for proper log levels
}

func corsMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

        if r.Method == "OPTIONS" {
            w.WriteHeader(http.StatusOK)
            return
        }

        next.ServeHTTP(w, r)
    })
}

func panicRecoveryMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        defer func() {
            if err := recover(); err != nil {
                log.Printf("[PANIC RECOVERY] Recovered from panic: %v", err)
                
                // Capture full stack trace including all goroutines to find the actual panic location
                // Use true to get all goroutines, which will include the panic location
                buf := make([]byte, 8192)
                n := runtime.Stack(buf, true)
                stackTrace := string(buf[:n])
                
                // Extract boardId
                boardId := extractBoardId(r)
                log.Printf("[PANIC RECOVERY] Extracted boardId: %s", func() string {
                    if boardId == "" { return "NULL" }
                    return boardId
                }())
                
                // Send error to runtime error endpoint if configured
                runtimeErrorEndpointUrl := os.Getenv("RUNTIME_ERROR_ENDPOINT_URL")
                if runtimeErrorEndpointUrl != "" {
                    log.Printf("[PANIC RECOVERY] Sending error to endpoint: %s", runtimeErrorEndpointUrl)
                    go sendErrorToEndpoint(runtimeErrorEndpointUrl, boardId, r, err, stackTrace)
                } else {
                    log.Printf("[PANIC RECOVERY] RUNTIME_ERROR_ENDPOINT_URL is not set - skipping error reporting")
                }
                
                // Return error response
                w.Header().Set("Content-Type", "application/json")
                w.WriteHeader(http.StatusInternalServerError)
                fmt.Fprintf(w, `{"error":"An error occurred while processing your request","message":"%s"}`, fmt.Sprintf("%v", err))
            }
        }()
        
        next.ServeHTTP(w, r)
    })
}

func extractBoardId(r *http.Request) string {
    // Try query parameter
    if boardId := r.URL.Query().Get("boardId"); boardId != "" {
        return boardId
    }
    
    // Try header
    if boardId := r.Header.Get("X-Board-Id"); boardId != "" {
        return boardId
    }
    
    // Try environment variable
    if boardId := os.Getenv("BOARD_ID"); boardId != "" {
        return boardId
    }
    
    // Try to extract from hostname (Railway pattern: webapi{boardId}.up.railway.app - no hyphen)
    host := r.Host
    if host != "" {
        // Simple regex-like matching using strings
        if idx := strings.Index(strings.ToLower(host), "webapi"); idx >= 0 {
            remaining := host[idx+6:] // Skip "webapi"
            if len(remaining) >= 24 {
                // Check if next 24 chars are hex
                boardId := remaining[:24]
                if isValidHex(boardId) {
                    return boardId
                }
            }
        }
    }
    
    // Try to extract from RUNTIME_ERROR_ENDPOINT_URL if it contains boardId pattern
    endpointUrl := os.Getenv("RUNTIME_ERROR_ENDPOINT_URL")
    if endpointUrl != "" {
        if idx := strings.Index(strings.ToLower(endpointUrl), "webapi"); idx >= 0 {
            remaining := endpointUrl[idx+6:]
            if len(remaining) >= 24 {
                boardId := remaining[:24]
                if isValidHex(boardId) {
                    return boardId
                }
            }
        }
    }
    
    return ""
}

func isValidHex(s string) bool {
    for _, c := range s {
        if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
            return false
        }
    }
    return true
}

func sendErrorToEndpoint(endpointUrl, boardId string, r *http.Request, err interface{}, stackTrace string) {
    // Parse stack trace to extract file and line number from the actual panic location
    // Go stack trace format: 
    // goroutine X [running]:
    // main.functionName(...)
    //     /path/to/file.go:123 +0x...
    var fileName string
    var lineNumber int
    
    lines := strings.Split(stackTrace, "\n")
    // Go stack trace format (with all goroutines):
    // goroutine X [running]:
    // main.panicRecoveryMiddleware.func1.1(...)
    //     /app/main.go:61 +0x...
    // goroutine Y [running]:
    // main.testController.GetAll(...)
    //     /app/Controllers/test_controller.go:33 +0x...
    // 
    // Look through all goroutines to find the actual panic location
    // Skip panic recovery and error sending functions
    for i, line := range lines {
        // Skip goroutine header lines
        if strings.HasPrefix(line, "goroutine") {
            continue
        }
        
        // Look for file:line entries
        if strings.Contains(line, ".go:") && i > 0 {
            // Get the previous line (function name)
            prevLine := ""
            if i > 0 {
                prevLine = lines[i-1]
            }
            
            // Skip if it's from panic recovery, error sending, or runtime functions
            if strings.Contains(prevLine, "panicRecoveryMiddleware") || 
               strings.Contains(prevLine, "sendErrorToEndpoint") ||
               strings.Contains(prevLine, "runtime.Stack") ||
               strings.Contains(prevLine, "runtime.gopanic") ||
               strings.Contains(prevLine, "created by") ||
               strings.Contains(prevLine, "panic(") {
                continue
            }
            
            // Extract file path and line number from the indented line
            // Format: "\t/path/to/file.go:123 +0x..."
            trimmedLine := strings.TrimSpace(line)
            parts := strings.Split(trimmedLine, ":")
            if len(parts) >= 2 {
                // Get file path (everything before the last ":")
                filePath := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ":"))
                
                // Skip standard library and runtime files
                // Check for common Go standard library paths
                if strings.Contains(filePath, "/runtime/") ||
                   strings.Contains(filePath, "/mise/installs/go/") ||
                   strings.Contains(filePath, "/src/runtime/") ||
                   strings.Contains(filePath, "/src/net/") ||
                   strings.Contains(filePath, "/src/syscall/") ||
                   strings.Contains(filePath, "/src/internal/") ||
                   strings.Contains(filePath, "/src/database/") ||
                   strings.Contains(filePath, "/usr/local/go/") ||
                   strings.Contains(filePath, "/usr/lib/go/") {
                    continue
                }
                
                // Get the last part which should be the line number (may have offset like "123 +0x9c")
                lineStr := strings.TrimSpace(parts[len(parts)-1])
                // Remove any offset info (e.g., " +0x9c")
                if spaceIdx := strings.Index(lineStr, " "); spaceIdx > 0 {
                    lineStr = lineStr[:spaceIdx]
                }
                if lineNum, parseErr := strconv.Atoi(lineStr); parseErr == nil {
                    lineNumber = lineNum
                    // Extract just the filename
                    if lastSlash := strings.LastIndex(filePath, "/"); lastSlash >= 0 {
                        fileName = filePath[lastSlash+1:]
                    } else {
                        fileName = filePath
                    }
                    // Found a valid file/line that's not in recovery or standard library - use it
                    break
                }
            }
        }
    }
    
    // Escape stack trace for JSON (handle newlines, backslashes, and quotes)
    escapedStackTrace := strings.ReplaceAll(stackTrace, `\`, `\\`)
    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, `"`, `\"`)
    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\n", `\n`)
    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\r", `\r`)
    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\t", `\t`)
    
    message := strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("%v", err), `\`, `\\`), `"`, `\"`)
    
    // Build payload with file and line information
    fileJson := "null"
    if fileName != "" {
        fileJson = `"` + strings.ReplaceAll(fileName, `"`, `\"`) + `"`
    }
    
    lineJson := "null"
    if lineNumber > 0 {
        lineJson = fmt.Sprintf("%d", lineNumber)
    }
    
    payload := fmt.Sprintf(`{
        "boardId":%s,
        "timestamp":"%s",
        "file":%s,
        "line":%s,
        "stackTrace":"%s",
        "message":"%s",
        "exceptionType":"panic",
        "requestPath":"%s",
        "requestMethod":"%s",
        "userAgent":"%s"
    }`,
        func() string {
            if boardId == "" { return "null" }
            return `"` + boardId + `"`
        }(),
        time.Now().UTC().Format(time.RFC3339),
        fileJson,
        lineJson,
        escapedStackTrace,
        message,
        r.URL.Path,
        r.Method,
        r.UserAgent(),
    )
    
    // Send POST request (fire and forget)
    req, err2 := http.NewRequest("POST", endpointUrl, strings.NewReader(payload))
    if err2 != nil {
        log.Printf("[PANIC RECOVERY] Failed to create request: %v", err2)
        return
    }
    
    req.Header.Set("Content-Type", "application/json")
    client := &http.Client{Timeout: 5 * time.Second}
    
    resp, err2 := client.Do(req)
    if err2 != nil {
        log.Printf("[PANIC RECOVERY] Failed to send error to endpoint: %v", err2)
        return
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        body, _ := io.ReadAll(resp.Body)
        log.Printf("[PANIC RECOVERY] Error endpoint response: %d - %s", resp.StatusCode, string(body))
    } else {
        log.Printf("[PANIC RECOVERY] Error endpoint response: %d", resp.StatusCode)
    }
}

func main() {
    databaseUrl := os.Getenv("DATABASE_URL")
    if databaseUrl == "" {
        log.Fatal("DATABASE_URL environment variable not set")
    }

    db, err := sql.Open("postgres", databaseUrl)
    if err != nil {
        log.Fatal("Failed to connect to database: ", err)
    }
    defer db.Close()

    if err := db.Ping(); err != nil {
        log.Fatal("Failed to ping database: ", err)
    }

    controller := controllers.NewTestController(db)
    mux := http.NewServeMux()

    // Apply panic recovery middleware to all routes
    handler := panicRecoveryMiddleware(corsMiddleware(mux))

    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        fmt.Fprintf(w, `{"message":"Backend API is running","status":"ok","swagger":"/swagger","api":"/api/test"}`)
    })

    mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        fmt.Fprintf(w, `{"status":"healthy","service":"Backend API"}`)
    })

    // Swagger UI endpoint - serve interactive Swagger UI HTML page
    mux.HandleFunc("/swagger", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/html")
        fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Backend API - Swagger UI</title>
    <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5.9.0/swagger-ui.css" />
    <style>
        html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin:0; background: #fafafa; }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.9.0/swagger-ui-bundle.js"></script>
    <script src="https://unpkg.com/swagger-ui-dist@5.9.0/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {
            const ui = SwaggerUIBundle({
                url: "/swagger.json",
                dom_id: "#swagger-ui",
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout"
            });
        };
    </script>
</body>
</html>`)
    })

    // Swagger JSON endpoint - return OpenAPI spec as JSON
    mux.HandleFunc("/swagger.json", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        fmt.Fprintf(w, `{
  "openapi": "3.0.0",
  "info": {
    "title": "Backend API",
    "version": "1.0.0",
    "description": "Go Backend API Documentation"
  },
  "paths": {
    "/api/test": {
      "get": {
        "summary": "Get all test projects",
        "responses": {
          "200": {
            "description": "List of test projects",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": {
                    "$ref": "#/components/schemas/TestProjects"
                  }
                }
              }
            }
          }
        }
      },
      "post": {
        "summary": "Create a new test project",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "$ref": "#/components/schemas/TestProjectsInput"
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Created test project",
            "content": {
              "application/json": {
                "schema": {
                  "$ref": "#/components/schemas/TestProjects"
                }
              }
            }
          }
        }
      }
    },
    "/api/test/{id}": {
      "get": {
        "summary": "Get test project by ID",
        "parameters": [
          {
            "name": "id",
            "in": "path",
            "required": true,
            "schema": {
              "type": "integer"
            }
          }
        ],
        "responses": {
          "200": {
            "description": "Test project found",
            "content": {
              "application/json": {
                "schema": {
                  "$ref": "#/components/schemas/TestProjects"
                }
              }
            }
          },
          "404": {
            "description": "Project not found"
          }
        }
      },
      "put": {
        "summary": "Update test project",
        "parameters": [
          {
            "name": "id",
            "in": "path",
            "required": true,
            "schema": {
              "type": "integer"
            }
          }
        ],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "$ref": "#/components/schemas/TestProjectsInput"
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Updated test project"
          },
          "404": {
            "description": "Project not found"
          }
        }
      },
      "delete": {
        "summary": "Delete test project",
        "parameters": [
          {
            "name": "id",
            "in": "path",
            "required": true,
            "schema": {
              "type": "integer"
            }
          }
        ],
        "responses": {
          "200": {
            "description": "Deleted successfully"
          },
          "404": {
            "description": "Project not found"
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "TestProjects": {
        "type": "object",
        "properties": {
          "Id": {
            "type": "integer"
          },
          "Name": {
            "type": "string"
          }
        }
      },
      "TestProjectsInput": {
        "type": "object",
        "required": ["Name"],
        "properties": {
          "Name": {
            "type": "string"
          }
        }
      }
    }
  }
}`)
    })

    // API routes handler function
    apiTestHandler := func(w http.ResponseWriter, r *http.Request) {
        path := r.URL.Path
        
        // Handle /api/test and /api/test/ (no ID) - normalize trailing slash
        if path == "/api/test" || path == "/api/test/" {
            switch r.Method {
            case "GET":
                controller.GetAll(w, r)
            case "POST":
                controller.Create(w, r)
            default:
                http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            }
            return
        }
        
        // Handle /api/test/:id
        if strings.HasPrefix(path, "/api/test/") {
            idStr := strings.TrimPrefix(path, "/api/test/")
            if idStr == "" {
                // Empty ID after /api/test/, treat as /api/test/
                switch r.Method {
                case "GET":
                    controller.GetAll(w, r)
                case "POST":
                    controller.Create(w, r)
                default:
                    http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
                }
                return
            }
            
            id, err := strconv.Atoi(idStr)
            if err != nil {
                http.Error(w, "Invalid ID", http.StatusBadRequest)
                return
            }
            
            switch r.Method {
            case "GET":
                controller.GetById(w, r, id)
            case "PUT":
                controller.Update(w, r, id)
            case "DELETE":
                controller.Delete(w, r, id)
            default:
                http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
            }
            return
        }
        
        http.NotFound(w, r)
    }

    // Register both /api/test and /api/test/ to handle trailing slashes
    mux.HandleFunc("/api/test", apiTestHandler)
    mux.HandleFunc("/api/test/", apiTestHandler)

    // Apply panic recovery middleware FIRST, then CORS middleware
    // Note: handler is already declared above, so use assignment instead of declaration
    handler = panicRecoveryMiddleware(corsMiddleware(mux))

    port := os.Getenv("PORT")
    if port == "" {
        port = "8080"
    }

    log.Printf("Server starting on 0.0.0.0:%s", port)
    
    // Declare variables for startup error handling (used in defer and error handler)
    runtimeErrorEndpointUrl := os.Getenv("RUNTIME_ERROR_ENDPOINT_URL")
    boardId := os.Getenv("BOARD_ID")
    
    // Startup error handler
    defer func() {
        if r := recover(); r != nil {
            log.Printf("[STARTUP ERROR] Application failed to start: %v", r)
            
            // Send startup error to endpoint (fire and forget)
            if runtimeErrorEndpointUrl != "" {
                go func() {
                    // Get full stack trace
                    buf := make([]byte, 4096)
                    n := runtime.Stack(buf, false)
                    stackTrace := string(buf[:n])
                    
                    // Parse stack trace to extract file and line number
                    var fileName string
                    var lineNumber int
                    
                    lines := strings.Split(stackTrace, "\n")
                    for i, line := range lines {
                        if strings.Contains(line, ".go:") && i > 0 {
                            parts := strings.Split(line, ":")
                            if len(parts) >= 2 {
                                lineStr := strings.TrimSpace(parts[len(parts)-1])
                                if lineNum, parseErr := strconv.Atoi(lineStr); parseErr == nil {
                                    lineNumber = lineNum
                                    filePath := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ":"))
                                    if lastSlash := strings.LastIndex(filePath, "/"); lastSlash >= 0 {
                                        fileName = filePath[lastSlash+1:]
                                    } else {
                                        fileName = filePath
                                    }
                                    break
                                }
                            }
                        }
                    }
                    
                    // Escape stack trace for JSON (handle newlines, backslashes, and quotes)
                    escapedStackTrace := strings.ReplaceAll(stackTrace, `\`, `\\`)
                    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, `"`, `\"`)
                    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\n", `\n`)
                    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\r", `\r`)
                    escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\t", `\t`)
                    
                    message := strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("%v", r), `\`, `\\`), `"`, `\"`)
                    
                    fileJson := "null"
                    if fileName != "" {
                        fileJson = `"` + strings.ReplaceAll(fileName, `"`, `\"`) + `"`
                    }
                    
                    lineJson := "null"
                    if lineNumber > 0 {
                        lineJson = fmt.Sprintf("%d", lineNumber)
                    }
                    
                    payload := fmt.Sprintf(`{
                        "boardId":%s,
                        "timestamp":"%s",
                        "file":%s,
                        "line":%s,
                        "stackTrace":"%s",
                        "message":"%s",
                        "exceptionType":"panic",
                        "requestPath":"STARTUP",
                        "requestMethod":"STARTUP",
                        "userAgent":"STARTUP_ERROR"
                    }`,
                        func() string {
                            if boardId == "" { return "null" }
                            return `"` + boardId + `"`
                        }(),
                        time.Now().UTC().Format(time.RFC3339),
                        fileJson,
                        lineJson,
                        escapedStackTrace,
                        message,
                    )
                    
                    req, err2 := http.NewRequest("POST", runtimeErrorEndpointUrl, strings.NewReader(payload))
                    if err2 != nil {
                        return
                    }
                    
                    req.Header.Set("Content-Type", "application/json")
                    client := &http.Client{Timeout: 5 * time.Second}
                    
                    client.Do(req) // Fire and forget
                }()
            }
            
            os.Exit(1)
        }
    }()
    
    if err = http.ListenAndServe("0.0.0.0:"+port, handler); err != nil {
        log.Printf("[STARTUP ERROR] Server failed to start: %v", err)
        
        // Send startup error to endpoint (same as above)
        // Note: runtimeErrorEndpointUrl and boardId are already declared above
        if runtimeErrorEndpointUrl != "" {
            go func() {
                // Get full stack trace
                buf := make([]byte, 4096)
                n := runtime.Stack(buf, false)
                stackTrace := string(buf[:n])
                
                // Parse stack trace to extract file and line number
                var fileName string
                var lineNumber int
                
                lines := strings.Split(stackTrace, "\n")
                for i, line := range lines {
                    if strings.Contains(line, ".go:") && i > 0 {
                        parts := strings.Split(line, ":")
                        if len(parts) >= 2 {
                            lineStr := strings.TrimSpace(parts[len(parts)-1])
                            if lineNum, parseErr := strconv.Atoi(lineStr); parseErr == nil {
                                lineNumber = lineNum
                                filePath := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ":"))
                                if lastSlash := strings.LastIndex(filePath, "/"); lastSlash >= 0 {
                                    fileName = filePath[lastSlash+1:]
                                } else {
                                    fileName = filePath
                                }
                                break
                            }
                        }
                    }
                }
                
                // Escape stack trace for JSON (handle newlines, backslashes, and quotes)
                escapedStackTrace := strings.ReplaceAll(stackTrace, `\`, `\\`)
                escapedStackTrace = strings.ReplaceAll(escapedStackTrace, `"`, `\"`)
                escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\n", `\n`)
                escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\r", `\r`)
                escapedStackTrace = strings.ReplaceAll(escapedStackTrace, "\t", `\t`)
                
                message := strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("%v", err), `\`, `\\`), `"`, `\"`)
                
                fileJson := "null"
                if fileName != "" {
                    fileJson = `"` + strings.ReplaceAll(fileName, `"`, `\"`) + `"`
                }
                
                lineJson := "null"
                if lineNumber > 0 {
                    lineJson = fmt.Sprintf("%d", lineNumber)
                }
                
                payload := fmt.Sprintf(`{
                    "boardId":%s,
                    "timestamp":"%s",
                    "file":%s,
                    "line":%s,
                    "stackTrace":"%s",
                    "message":"%s",
                    "exceptionType":"error",
                    "requestPath":"STARTUP",
                    "requestMethod":"STARTUP",
                    "userAgent":"STARTUP_ERROR"
                }`,
                    func() string {
                        if boardId == "" { return "null" }
                        return `"` + boardId + `"`
                    }(),
                    time.Now().UTC().Format(time.RFC3339),
                    fileJson,
                    lineJson,
                    escapedStackTrace,
                    message,
                )
                
                req, err2 := http.NewRequest("POST", runtimeErrorEndpointUrl, strings.NewReader(payload))
                if err2 != nil {
                    return
                }
                
                req.Header.Set("Content-Type", "application/json")
                client := &http.Client{Timeout: 5 * time.Second}
                
                client.Do(req) // Fire and forget
            }()
        }
        
        os.Exit(1)
    }
}
