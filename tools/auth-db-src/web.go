package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

//go:embed ui
var uiFiles embed.FS

func serveCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a local web UI for managing the auth database",
		Long: `Start a local HTTP server on 127.0.0.1 with a browser UI for managing
roles, API keys, permissions, and trusted users.

The server binds to 127.0.0.1 only and is not accessible from the network.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(port)
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 8765, "Port to listen on (127.0.0.1 only)")
	return cmd
}

type apiServer struct{ db *sql.DB }

func runServe(port int) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	s := &apiServer{db: db}
	mux := http.NewServeMux()

	// Static UI (embedded)
	uiFS, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("/", http.FileServer(http.FS(uiFS)))

	// JSON API
	mux.HandleFunc("/api/info", s.handleInfo)
	mux.HandleFunc("/api/roles", s.handleRoles)
	mux.HandleFunc("/api/roles/", s.handleRoleByName)
	mux.HandleFunc("/api/keys", s.handleKeys)
	mux.HandleFunc("/api/keys/", s.handleKeyByValue)
	mux.HandleFunc("/api/permissions", s.handlePermissions)
	mux.HandleFunc("/api/permissions/", s.handlePermissionByRoleTable)
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/users/", s.handleUserByName)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Printf("Auth DB UI → http://%s\n", addr)
	fmt.Printf("Database   → %s\n", dbPath)
	fmt.Println("Press Ctrl+C to stop.")

	return http.ListenAndServe(addr, mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// GET /api/info
func (s *apiServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var roles, keys, activeKeys, perms, users, activeUsers int
	s.db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roles)
	s.db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&keys)
	s.db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE is_active").Scan(&activeKeys)
	s.db.QueryRow("SELECT COUNT(*) FROM permissions").Scan(&perms)
	s.db.QueryRow("SELECT COUNT(*) FROM trusted_users").Scan(&users)
	s.db.QueryRow("SELECT COUNT(*) FROM trusted_users WHERE is_active").Scan(&activeUsers)
	writeJSON(w, http.StatusOK, map[string]any{
		"db_path":      dbPath,
		"roles":        roles,
		"keys":         keys,
		"active_keys":  activeKeys,
		"permissions":  perms,
		"users":        users,
		"active_users": activeUsers,
	})
}

// GET /api/roles  POST /api/roles
func (s *apiServer) handleRoles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.db.Query("SELECT role_name, COALESCE(description,'') FROM roles ORDER BY role_name")
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		defer rows.Close()
		type Role struct {
			Name string `json:"name"`
			Desc string `json:"description"`
		}
		out := []Role{}
		for rows.Next() {
			var ro Role
			rows.Scan(&ro.Name, &ro.Desc)
			out = append(out, ro)
		}
		writeJSON(w, 200, out)

	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
			Desc string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			writeError(w, 400, "name is required")
			return
		}
		_, err := s.db.Exec("INSERT INTO roles (role_name, description) VALUES (?, ?)", body.Name, body.Desc)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "Duplicate") {
				writeError(w, 409, "role already exists")
				return
			}
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, map[string]string{"name": body.Name})

	default:
		writeError(w, 405, "method not allowed")
	}
}

// DELETE /api/roles/{name}?force=true
func (s *apiServer) handleRoleByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, 405, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/roles/")
	if name == "" {
		writeError(w, 400, "role name required")
		return
	}
	force := r.URL.Query().Get("force") == "true"

	var keyCount, permCount int
	s.db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE role_name = ?", name).Scan(&keyCount)
	s.db.QueryRow("SELECT COUNT(*) FROM permissions WHERE role_name = ?", name).Scan(&permCount)

	if (keyCount > 0 || permCount > 0) && !force {
		writeError(w, 409, fmt.Sprintf("role has %d key(s) and %d permission(s); use force=true to also remove them", keyCount, permCount))
		return
	}
	if force {
		s.db.Exec("DELETE FROM api_keys WHERE role_name = ?", name)
		s.db.Exec("DELETE FROM permissions WHERE role_name = ?", name)
	}
	res, err := s.db.Exec("DELETE FROM roles WHERE role_name = ?", name)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "role not found")
		return
	}
	writeJSON(w, 200, map[string]string{"deleted": name})
}

// GET /api/keys  POST /api/keys
func (s *apiServer) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.db.Query(`
			SELECT key, role_name, COALESCE(note,''), created_at, expires_at, is_active
			FROM api_keys ORDER BY created_at DESC`)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		defer rows.Close()
		type Key struct {
			Key       string  `json:"key"`
			Role      string  `json:"role"`
			Note      string  `json:"note"`
			CreatedAt string  `json:"created_at"`
			ExpiresAt *string `json:"expires_at"`
			Active    bool    `json:"active"`
		}
		out := []Key{}
		for rows.Next() {
			var k Key
			var createdAt time.Time
			var expiresAt sql.NullTime
			rows.Scan(&k.Key, &k.Role, &k.Note, &createdAt, &expiresAt, &k.Active)
			k.CreatedAt = createdAt.Format("2006-01-02")
			if expiresAt.Valid {
				s := expiresAt.Time.Format("2006-01-02")
				k.ExpiresAt = &s
			}
			out = append(out, k)
		}
		writeJSON(w, 200, out)

	case http.MethodPost:
		var body struct {
			Role    string `json:"role"`
			Key     string `json:"key"`
			Expires string `json:"expires"`
			Note    string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Role == "" {
			writeError(w, 400, "role is required")
			return
		}
		var roleExists bool
		if err := s.db.QueryRow("SELECT 1 FROM roles WHERE role_name = ?", body.Role).Scan(&roleExists); err == sql.ErrNoRows {
			writeError(w, 404, "role not found")
			return
		}
		key := body.Key
		if key == "" {
			var err error
			key, err = generateRandomKey()
			if err != nil {
				writeError(w, 500, err.Error())
				return
			}
		}
		var expiresAt *time.Time
		if body.Expires != "" {
			t, err := time.Parse(time.RFC3339, body.Expires)
			if err != nil {
				writeError(w, 400, "invalid expires format — use RFC3339 e.g. 2026-12-31T23:59:59Z")
				return
			}
			expiresAt = &t
		}
		var noteVal any
		if body.Note != "" {
			noteVal = body.Note
		}
		_, err := s.db.Exec("INSERT INTO api_keys (key, role_name, note, expires_at) VALUES (?, ?, ?, ?)", key, body.Role, noteVal, expiresAt)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeError(w, 409, "key already exists")
				return
			}
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, map[string]string{"key": key, "role": body.Role})

	default:
		writeError(w, 405, "method not allowed")
	}
}

// DELETE /api/keys/{key}
func (s *apiServer) handleKeyByValue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, 405, "method not allowed")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	if key == "" {
		writeError(w, 400, "key required")
		return
	}
	res, err := s.db.Exec("DELETE FROM api_keys WHERE key = ?", key)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "key not found")
		return
	}
	writeJSON(w, 200, map[string]string{"deleted": "ok"})
}

// GET /api/permissions?role=x  POST /api/permissions
func (s *apiServer) handlePermissions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		role := r.URL.Query().Get("role")
		query := `SELECT role_name, table_name, can_create, can_read, can_update, can_delete, can_query,
			COALESCE(can_execute, false) FROM permissions`
		var args []any
		if role != "" {
			query += " WHERE role_name = ?"
			args = append(args, role)
		}
		query += " ORDER BY role_name, table_name"
		rows, err := s.db.Query(query, args...)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		defer rows.Close()
		type Perm struct {
			Role       string `json:"role"`
			Table      string `json:"table"`
			CanCreate  bool   `json:"can_create"`
			CanRead    bool   `json:"can_read"`
			CanUpdate  bool   `json:"can_update"`
			CanDelete  bool   `json:"can_delete"`
			CanQuery   bool   `json:"can_query"`
			CanExecute bool   `json:"can_execute"`
		}
		out := []Perm{}
		for rows.Next() {
			var p Perm
			rows.Scan(&p.Role, &p.Table, &p.CanCreate, &p.CanRead, &p.CanUpdate, &p.CanDelete, &p.CanQuery, &p.CanExecute)
			out = append(out, p)
		}
		writeJSON(w, 200, out)

	case http.MethodPost:
		var body struct {
			Role       string `json:"role"`
			Table      string `json:"table"`
			CanCreate  bool   `json:"can_create"`
			CanRead    bool   `json:"can_read"`
			CanUpdate  bool   `json:"can_update"`
			CanDelete  bool   `json:"can_delete"`
			CanQuery   bool   `json:"can_query"`
			CanExecute bool   `json:"can_execute"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Role == "" || body.Table == "" {
			writeError(w, 400, "role and table are required")
			return
		}
		_, err := s.db.Exec(`
			INSERT INTO permissions (id, role_name, table_name, can_create, can_read, can_update, can_delete, can_query, can_execute)
			VALUES (nextval('permissions_id_seq'), ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (role_name, table_name) DO UPDATE SET
				can_create=EXCLUDED.can_create, can_read=EXCLUDED.can_read,
				can_update=EXCLUDED.can_update, can_delete=EXCLUDED.can_delete,
				can_query=EXCLUDED.can_query, can_execute=EXCLUDED.can_execute`,
			body.Role, body.Table,
			body.CanCreate, body.CanRead, body.CanUpdate, body.CanDelete, body.CanQuery, body.CanExecute)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"role": body.Role, "table": body.Table})

	default:
		writeError(w, 405, "method not allowed")
	}
}

// DELETE /api/permissions/{role}/{table}
func (s *apiServer) handlePermissionByRoleTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, 405, "method not allowed")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/permissions/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeError(w, 400, "path must be /api/permissions/{role}/{table}")
		return
	}
	role, table := parts[0], parts[1]
	res, err := s.db.Exec("DELETE FROM permissions WHERE role_name = ? AND table_name = ?", role, table)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "permission not found")
		return
	}
	writeJSON(w, 200, map[string]string{"deleted": "ok"})
}

// GET /api/users  POST /api/users
func (s *apiServer) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.db.Query(`
			SELECT username, role_name, COALESCE(note,''), is_active, created_at
			FROM trusted_users ORDER BY username`)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		defer rows.Close()
		type User struct {
			Username  string `json:"username"`
			Role      string `json:"role"`
			Note      string `json:"note"`
			Active    bool   `json:"active"`
			CreatedAt string `json:"created_at"`
		}
		out := []User{}
		for rows.Next() {
			var u User
			var createdAt time.Time
			rows.Scan(&u.Username, &u.Role, &u.Note, &u.Active, &createdAt)
			u.CreatedAt = createdAt.Format("2006-01-02")
			out = append(out, u)
		}
		writeJSON(w, 200, out)

	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Role     string `json:"role"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Role == "" {
			writeError(w, 400, "username and role are required")
			return
		}
		var noteVal any
		if body.Note != "" {
			noteVal = body.Note
		}
		_, err := s.db.Exec(
			"INSERT INTO trusted_users (username, role_name, note) VALUES (?, ?, ?)",
			body.Username, body.Role, noteVal)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeError(w, 409, "user already exists")
				return
			}
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, map[string]string{"username": body.Username})

	default:
		writeError(w, 405, "method not allowed")
	}
}

// DELETE /api/users/{username}
func (s *apiServer) handleUserByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, 405, "method not allowed")
		return
	}
	username := strings.TrimPrefix(r.URL.Path, "/api/users/")
	if username == "" {
		writeError(w, 400, "username required")
		return
	}
	res, err := s.db.Exec("DELETE FROM trusted_users WHERE username = ?", username)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "user not found")
		return
	}
	writeJSON(w, 200, map[string]string{"deleted": username})
}
