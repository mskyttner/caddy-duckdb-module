package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	dbPath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "auth-db",
		Short: "Auth database management tool for Caddy DuckDB extension",
		Long: `A CLI tool to manage the authentication database for the Caddy DuckDB extension.

This tool allows you to:
  - Initialize a new auth database with the required schema
  - Manage roles (add, remove, list)
  - Manage API keys (add, remove, list)
  - Manage permissions (add, remove, list)

The created database can be mounted into containers via volume mounts.`,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&dbPath, "db", "d", "", "Path to auth database (required)")
	rootCmd.MarkPersistentFlagRequired("db")

	// Add subcommands
	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(migrateCmd())
	rootCmd.AddCommand(roleCmd())
	rootCmd.AddCommand(keyCmd())
	rootCmd.AddCommand(permissionCmd())
	rootCmd.AddCommand(infoCmd())
	rootCmd.AddCommand(userCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// migrateCmd creates the migrate subcommand
func migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply schema migrations to an existing auth database",
		Long: `Apply incremental schema migrations to bring an existing auth database
up to date with the current version of the Caddy DuckDB module.

Safe to run multiple times (idempotent). Currently applies:
  - Add can_execute column to permissions table (enables POST /duckdb/execute)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrate()
		},
	}
}

func runMigrate() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Migration: add can_execute column to permissions (idempotent via IF NOT EXISTS)
	_, err = db.Exec(`ALTER TABLE permissions ADD COLUMN IF NOT EXISTS can_execute BOOLEAN DEFAULT false`)
	if err != nil {
		return fmt.Errorf("failed to add can_execute column: %w", err)
	}
	fmt.Println("✓ permissions.can_execute column present")

	// Grant can_execute to admin role on wildcard if not already set
	_, err = db.Exec(`UPDATE permissions SET can_execute = true WHERE role_name = 'admin' AND table_name = '*'`)
	if err != nil {
		return fmt.Errorf("failed to update admin execute permission: %w", err)
	}
	fmt.Println("✓ admin role granted execute permission")
	fmt.Println("\nMigration complete. Restart the Caddy server to apply changes.")
	return nil
}

// initCmd creates the init subcommand
func initCmd() *cobra.Command {
	var withDefaults bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new auth database",
		Long: `Create a new auth database with the required schema.

By default, creates the schema with default roles (admin, editor, reader)
and their associated permissions. Use --no-defaults to create an empty schema.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(withDefaults)
		},
	}

	cmd.Flags().BoolVar(&withDefaults, "with-defaults", true, "Include default roles and permissions")

	return cmd
}

// roleCmd creates the role subcommand with add/remove/list
func roleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "Manage roles",
	}

	// role add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new role",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			desc, _ := cmd.Flags().GetString("desc")
			return runRoleAdd(name, desc)
		},
	}
	addCmd.Flags().StringP("name", "n", "", "Role name (required)")
	addCmd.Flags().StringP("desc", "", "", "Role description")
	addCmd.MarkFlagRequired("name")

	// role remove
	removeCmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a role",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			force, _ := cmd.Flags().GetBool("force")
			return runRoleRemove(name, force)
		},
	}
	removeCmd.Flags().StringP("name", "n", "", "Role name (required)")
	removeCmd.Flags().BoolP("force", "f", false, "Force removal (also removes associated API keys and permissions)")
	removeCmd.MarkFlagRequired("name")

	// role list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all roles",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoleList()
		},
	}

	cmd.AddCommand(addCmd, removeCmd, listCmd)
	return cmd
}

// keyCmd creates the key subcommand with add/remove/list
func keyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage API keys",
	}

	// key add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			role, _ := cmd.Flags().GetString("role")
			key, _ := cmd.Flags().GetString("key")
			expires, _ := cmd.Flags().GetString("expires")
			note, _ := cmd.Flags().GetString("note")
			return runKeyAdd(role, key, expires, note)
		},
	}
	addCmd.Flags().StringP("role", "r", "", "Role name (required)")
	addCmd.Flags().StringP("key", "k", "", "API key (if empty, generates a random one)")
	addCmd.Flags().StringP("expires", "e", "", "Expiration date (RFC3339 format, e.g., 2025-12-31T23:59:59Z)")
	addCmd.Flags().StringP("note", "n", "", "Note to associate with the key (e.g., user name or purpose)")
	addCmd.MarkFlagRequired("role")

	// key remove
	removeCmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			key, _ := cmd.Flags().GetString("key")
			return runKeyRemove(key)
		},
	}
	removeCmd.Flags().StringP("key", "k", "", "API key to remove (required)")
	removeCmd.MarkFlagRequired("key")

	// key list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			showKeys, _ := cmd.Flags().GetBool("show-keys")
			return runKeyList(showKeys)
		},
	}
	listCmd.Flags().Bool("show-keys", false, "Show full API keys (by default only shows first 8 characters)")

	cmd.AddCommand(addCmd, removeCmd, listCmd)
	return cmd
}

// permissionCmd creates the permission subcommand with add/remove/list
func permissionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "permission",
		Short:   "Manage permissions",
		Aliases: []string{"perm"},
	}

	// permission add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a permission for a role on a table",
		Long: `Add permissions for a role on a specific table or all tables (*).

Operations can be specified as a comma-separated list:
  - create (or c): Allow INSERT operations
  - read (or r): Allow SELECT operations
  - update (or u): Allow UPDATE operations
  - delete (or d): Allow DELETE operations
  - query (or q): Allow raw SQL queries
  - all: All operations
  - crud: create, read, update, delete (no query)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			role, _ := cmd.Flags().GetString("role")
			table, _ := cmd.Flags().GetString("table")
			ops, _ := cmd.Flags().GetString("operations")
			return runPermissionAdd(role, table, ops)
		},
	}
	addCmd.Flags().StringP("role", "r", "", "Role name (required)")
	addCmd.Flags().StringP("table", "t", "", "Table name or * for all tables (required)")
	addCmd.Flags().StringP("operations", "o", "", "Operations to allow: c,r,u,d,q or create,read,update,delete,query or all,crud (required)")
	addCmd.MarkFlagRequired("role")
	addCmd.MarkFlagRequired("table")
	addCmd.MarkFlagRequired("operations")

	// permission remove
	removeCmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a permission",
		RunE: func(cmd *cobra.Command, args []string) error {
			role, _ := cmd.Flags().GetString("role")
			table, _ := cmd.Flags().GetString("table")
			return runPermissionRemove(role, table)
		},
	}
	removeCmd.Flags().StringP("role", "r", "", "Role name (required)")
	removeCmd.Flags().StringP("table", "t", "", "Table name (required)")
	removeCmd.MarkFlagRequired("role")
	removeCmd.MarkFlagRequired("table")

	// permission list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all permissions",
		RunE: func(cmd *cobra.Command, args []string) error {
			role, _ := cmd.Flags().GetString("role")
			return runPermissionList(role)
		},
	}
	listCmd.Flags().StringP("role", "r", "", "Filter by role name (optional)")

	cmd.AddCommand(addCmd, removeCmd, listCmd)
	return cmd
}

// infoCmd creates the info subcommand
func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show database information and statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInfo()
		},
	}
}

// openDB opens the database connection
func openDB() (*sql.DB, error) {
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	return db, nil
}

// runInit initializes the auth database
func runInit(withDefaults bool) error {
	// Check if file already exists
	if _, err := os.Stat(dbPath); err == nil {
		return fmt.Errorf("database already exists at %s (remove it first or use a different path)", dbPath)
	}

	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Create schema
	schema := `
		-- Roles table (must be created first due to foreign key constraints)
		CREATE TABLE IF NOT EXISTS roles (
			role_name VARCHAR PRIMARY KEY,
			description VARCHAR
		);

		-- API Keys table
		CREATE TABLE IF NOT EXISTS api_keys (
			key VARCHAR PRIMARY KEY,
			role_name VARCHAR NOT NULL,
			note VARCHAR,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP,
			is_active BOOLEAN DEFAULT true,
			FOREIGN KEY (role_name) REFERENCES roles(role_name)
		);

		-- Permissions table
		CREATE TABLE IF NOT EXISTS permissions (
			id INTEGER PRIMARY KEY,
			role_name VARCHAR NOT NULL,
			table_name VARCHAR NOT NULL,
			can_create BOOLEAN DEFAULT false,
			can_read BOOLEAN DEFAULT false,
			can_update BOOLEAN DEFAULT false,
			can_delete BOOLEAN DEFAULT false,
			can_query BOOLEAN DEFAULT false,
			can_execute BOOLEAN DEFAULT false,
			FOREIGN KEY (role_name) REFERENCES roles(role_name),
			UNIQUE(role_name, table_name)
		);

		-- Create sequence for permissions ID
		CREATE SEQUENCE IF NOT EXISTS permissions_id_seq START 1;

		-- Trusted users table (for vouch-proxy / forward_auth integration)
		CREATE TABLE IF NOT EXISTS trusted_users (
			username   VARCHAR PRIMARY KEY,
			role_name  VARCHAR NOT NULL,
			note       VARCHAR,
			is_active  BOOLEAN DEFAULT true,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (role_name) REFERENCES roles(role_name)
		);
	`

	_, err = db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	fmt.Printf("✓ Created auth database at %s\n", dbPath)

	if withDefaults {
		// Insert default roles
		defaults := `
			-- Default roles
			INSERT INTO roles (role_name, description)
			VALUES ('admin', 'Full access to all tables and raw SQL queries');

			INSERT INTO roles (role_name, description)
			VALUES ('editor', 'CRUD access to all tables, no raw SQL');

			INSERT INTO roles (role_name, description)
			VALUES ('reader', 'Read-only access to all tables');

			-- Default permissions
			INSERT INTO permissions (id, role_name, table_name, can_create, can_read, can_update, can_delete, can_query, can_execute)
			VALUES (nextval('permissions_id_seq'), 'admin', '*', true, true, true, true, true, true);

			INSERT INTO permissions (id, role_name, table_name, can_create, can_read, can_update, can_delete, can_query, can_execute)
			VALUES (nextval('permissions_id_seq'), 'editor', '*', true, true, true, true, false, false);

			INSERT INTO permissions (id, role_name, table_name, can_create, can_read, can_update, can_delete, can_query, can_execute)
			VALUES (nextval('permissions_id_seq'), 'reader', '*', false, true, false, false, false, false);
		`

		_, err = db.Exec(defaults)
		if err != nil {
			return fmt.Errorf("failed to insert defaults: %w", err)
		}

		fmt.Println("✓ Added default roles: admin, editor, reader")
		fmt.Println("✓ Added default permissions")
	}

	fmt.Println("")
	fmt.Println("Next steps:")
	fmt.Println("  1. Create an API key:  auth-db key add -d " + dbPath + " -r admin")
	fmt.Println("  2. Mount the database into your container")

	return nil
}

// runRoleAdd adds a new role
func runRoleAdd(name, desc string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO roles (role_name, description) VALUES (?, ?)", name, desc)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "Duplicate") {
			return fmt.Errorf("role '%s' already exists", name)
		}
		return fmt.Errorf("failed to create role: %w", err)
	}

	fmt.Printf("✓ Created role '%s'\n", name)
	return nil
}

// runRoleRemove removes a role
func runRoleRemove(name string, force bool) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Check if role has associated API keys or permissions
	var keyCount, permCount int
	db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE role_name = ?", name).Scan(&keyCount)
	db.QueryRow("SELECT COUNT(*) FROM permissions WHERE role_name = ?", name).Scan(&permCount)

	if (keyCount > 0 || permCount > 0) && !force {
		return fmt.Errorf("role '%s' has %d API keys and %d permissions; use --force to remove", name, keyCount, permCount)
	}

	if force {
		// Delete associated API keys and permissions first
		db.Exec("DELETE FROM api_keys WHERE role_name = ?", name)
		db.Exec("DELETE FROM permissions WHERE role_name = ?", name)
	}

	result, err := db.Exec("DELETE FROM roles WHERE role_name = ?", name)
	if err != nil {
		return fmt.Errorf("failed to delete role: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("role '%s' not found", name)
	}

	fmt.Printf("✓ Removed role '%s'", name)
	if force && (keyCount > 0 || permCount > 0) {
		fmt.Printf(" (also removed %d API keys and %d permissions)", keyCount, permCount)
	}
	fmt.Println()

	return nil
}

// runRoleList lists all roles
func runRoleList() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query("SELECT role_name, COALESCE(description, '') FROM roles ORDER BY role_name")
	if err != nil {
		return fmt.Errorf("failed to query roles: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROLE\tDESCRIPTION")
	fmt.Fprintln(w, "----\t-----------")

	count := 0
	for rows.Next() {
		var name, desc string
		rows.Scan(&name, &desc)
		fmt.Fprintf(w, "%s\t%s\n", name, desc)
		count++
	}
	w.Flush()

	if count == 0 {
		fmt.Println("No roles found.")
	}

	return nil
}

// generateRandomKey generates a cryptographically secure random API key
func generateRandomKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// runKeyAdd adds a new API key
func runKeyAdd(role, key, expires, note string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Verify role exists
	var exists bool
	err = db.QueryRow("SELECT 1 FROM roles WHERE role_name = ?", role).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("role '%s' does not exist", role)
	}

	// Generate key if not provided
	if key == "" {
		key, err = generateRandomKey()
		if err != nil {
			return fmt.Errorf("failed to generate API key: %w", err)
		}
	}

	// Parse expiration if provided
	var expiresAt *time.Time
	if expires != "" {
		t, err := time.Parse(time.RFC3339, expires)
		if err != nil {
			return fmt.Errorf("invalid expiration date (use RFC3339 format, e.g., 2025-12-31T23:59:59Z): %w", err)
		}
		expiresAt = &t
	}

	// Handle note - use NULL if empty
	var noteValue interface{}
	if note != "" {
		noteValue = note
	}

	_, err = db.Exec("INSERT INTO api_keys (key, role_name, note, expires_at) VALUES (?, ?, ?, ?)", key, role, noteValue, expiresAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "Duplicate") {
			return fmt.Errorf("API key already exists")
		}
		return fmt.Errorf("failed to create API key: %w", err)
	}

	fmt.Println("✓ API key created successfully!")
	fmt.Println()
	fmt.Printf("  API Key:  %s\n", key)
	fmt.Printf("  Role:     %s\n", role)
	if note != "" {
		fmt.Printf("  Note:     %s\n", note)
	}
	fmt.Printf("  Created:  %s\n", time.Now().Format(time.RFC3339))
	if expiresAt != nil {
		fmt.Printf("  Expires:  %s\n", expiresAt.Format(time.RFC3339))
	} else {
		fmt.Printf("  Expires:  never\n")
	}
	fmt.Println()
	fmt.Println("Use this in your requests:")
	fmt.Printf("  curl -H \"X-API-Key: %s\" ...\n", key)

	return nil
}

// runKeyRemove removes an API key
func runKeyRemove(key string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := db.Exec("DELETE FROM api_keys WHERE key = ?", key)
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("API key not found")
	}

	fmt.Println("✓ API key removed")
	return nil
}

// runKeyList lists all API keys
func runKeyList(showKeys bool) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT key, role_name, COALESCE(note, ''), created_at, expires_at, is_active
		FROM api_keys
		ORDER BY created_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query API keys: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tROLE\tNOTE\tCREATED\tEXPIRES\tACTIVE")
	fmt.Fprintln(w, "---\t----\t----\t-------\t-------\t------")

	count := 0
	for rows.Next() {
		var key, role, note string
		var createdAt time.Time
		var expiresAt sql.NullTime
		var isActive bool
		rows.Scan(&key, &role, &note, &createdAt, &expiresAt, &isActive)

		displayKey := key
		if !showKeys && len(key) > 8 {
			displayKey = key[:8] + "..."
		}

		expiresStr := "never"
		if expiresAt.Valid {
			expiresStr = expiresAt.Time.Format("2006-01-02")
		}

		activeStr := "yes"
		if !isActive {
			activeStr = "no"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			displayKey,
			role,
			note,
			createdAt.Format("2006-01-02"),
			expiresStr,
			activeStr,
		)
		count++
	}
	w.Flush()

	if count == 0 {
		fmt.Println("No API keys found.")
	}

	return nil
}

// parseOperations parses operation flags into boolean values
func parseOperations(ops string) (canCreate, canRead, canUpdate, canDelete, canQuery, canExecute bool, err error) {
	ops = strings.ToLower(strings.TrimSpace(ops))

	if ops == "all" {
		return true, true, true, true, true, true, nil
	}
	if ops == "crud" {
		return true, true, true, true, false, false, nil
	}

	parts := strings.Split(ops, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "c", "create":
			canCreate = true
		case "r", "read":
			canRead = true
		case "u", "update":
			canUpdate = true
		case "d", "delete":
			canDelete = true
		case "q", "query":
			canQuery = true
		case "e", "execute":
			canExecute = true
		default:
			return false, false, false, false, false, false, fmt.Errorf("unknown operation: %s (valid: c/create, r/read, u/update, d/delete, q/query, e/execute, all, crud)", p)
		}
	}

	return canCreate, canRead, canUpdate, canDelete, canQuery, canExecute, nil
}

// runPermissionAdd adds a permission
func runPermissionAdd(role, table, ops string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Verify role exists
	var exists bool
	err = db.QueryRow("SELECT 1 FROM roles WHERE role_name = ?", role).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("role '%s' does not exist", role)
	}

	canCreate, canRead, canUpdate, canDelete, canQuery, canExecute, err := parseOperations(ops)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		INSERT INTO permissions (id, role_name, table_name, can_create, can_read, can_update, can_delete, can_query, can_execute)
		VALUES (nextval('permissions_id_seq'), ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (role_name, table_name) DO UPDATE SET
			can_create = EXCLUDED.can_create,
			can_read = EXCLUDED.can_read,
			can_update = EXCLUDED.can_update,
			can_delete = EXCLUDED.can_delete,
			can_query = EXCLUDED.can_query,
			can_execute = EXCLUDED.can_execute
	`, role, table, canCreate, canRead, canUpdate, canDelete, canQuery, canExecute)
	if err != nil {
		return fmt.Errorf("failed to create permission: %w", err)
	}

	fmt.Printf("✓ Permission set for role '%s' on table '%s'\n", role, table)
	fmt.Printf("  Create: %v, Read: %v, Update: %v, Delete: %v, Query: %v, Execute: %v\n",
		canCreate, canRead, canUpdate, canDelete, canQuery, canExecute)

	return nil
}

// runPermissionRemove removes a permission
func runPermissionRemove(role, table string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := db.Exec("DELETE FROM permissions WHERE role_name = ? AND table_name = ?", role, table)
	if err != nil {
		return fmt.Errorf("failed to delete permission: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("permission not found for role '%s' on table '%s'", role, table)
	}

	fmt.Printf("✓ Permission removed for role '%s' on table '%s'\n", role, table)
	return nil
}

// runPermissionList lists permissions
func runPermissionList(role string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	query := `SELECT role_name, table_name, can_create, can_read, can_update, can_delete, can_query,
		COALESCE(can_execute, false) AS can_execute FROM permissions`
	var args []interface{}
	if role != "" {
		query += " WHERE role_name = ?"
		args = append(args, role)
	}
	query += " ORDER BY role_name, table_name"

	rows, err := db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("failed to query permissions: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROLE\tTABLE\tCREATE\tREAD\tUPDATE\tDELETE\tQUERY\tEXECUTE")
	fmt.Fprintln(w, "----\t-----\t------\t----\t------\t------\t-----\t-------")

	count := 0
	for rows.Next() {
		var roleName, tableName string
		var canCreate, canRead, canUpdate, canDelete, canQuery, canExecute bool
		rows.Scan(&roleName, &tableName, &canCreate, &canRead, &canUpdate, &canDelete, &canQuery, &canExecute)
		fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%v\t%v\t%v\t%v\n",
			roleName, tableName, canCreate, canRead, canUpdate, canDelete, canQuery, canExecute)
		count++
	}
	w.Flush()

	if count == 0 {
		fmt.Println("No permissions found.")
	}

	return nil
}

// userCmd creates the user subcommand with add/remove/list for trusted users
func userCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage trusted users (vouch-proxy / forward_auth integration)",
	}

	// user add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a trusted user",
		RunE: func(cmd *cobra.Command, args []string) error {
			username, _ := cmd.Flags().GetString("username")
			role, _ := cmd.Flags().GetString("role")
			note, _ := cmd.Flags().GetString("note")
			return runUserAdd(username, role, note)
		},
	}
	addCmd.Flags().StringP("username", "u", "", "Username / identity (e.g. alice@example.com) (required)")
	addCmd.Flags().StringP("role", "r", "", "Role name (required)")
	addCmd.Flags().StringP("note", "n", "", "Optional note")
	addCmd.MarkFlagRequired("username")
	addCmd.MarkFlagRequired("role")

	// user remove
	removeCmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a trusted user",
		RunE: func(cmd *cobra.Command, args []string) error {
			username, _ := cmd.Flags().GetString("username")
			return runUserRemove(username)
		},
	}
	removeCmd.Flags().StringP("username", "u", "", "Username to remove (required)")
	removeCmd.MarkFlagRequired("username")

	// user list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all trusted users",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUserList()
		},
	}

	cmd.AddCommand(addCmd, removeCmd, listCmd)
	return cmd
}

// runUserAdd adds a trusted user
func runUserAdd(username, role, note string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	// Verify role exists
	var exists bool
	err = db.QueryRow("SELECT 1 FROM roles WHERE role_name = ?", role).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("role '%s' does not exist", role)
	}

	var noteValue interface{}
	if note != "" {
		noteValue = note
	}

	_, err = db.Exec(
		"INSERT INTO trusted_users (username, role_name, note) VALUES (?, ?, ?)",
		username, role, noteValue,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "Duplicate") {
			return fmt.Errorf("trusted user '%s' already exists", username)
		}
		return fmt.Errorf("failed to add trusted user: %w", err)
	}

	fmt.Printf("✓ Added trusted user '%s' with role '%s'\n", username, role)
	return nil
}

// runUserRemove removes a trusted user
func runUserRemove(username string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := db.Exec("DELETE FROM trusted_users WHERE username = ?", username)
	if err != nil {
		return fmt.Errorf("failed to remove trusted user: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("trusted user '%s' not found", username)
	}

	fmt.Printf("✓ Removed trusted user '%s'\n", username)
	return nil
}

// runUserList lists all trusted users
func runUserList() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT username, role_name, COALESCE(note, ''), is_active, created_at
		FROM trusted_users
		ORDER BY username
	`)
	if err != nil {
		return fmt.Errorf("failed to query trusted users: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tROLE\tNOTE\tACTIVE\tCREATED")
	fmt.Fprintln(w, "--------\t----\t----\t------\t-------")

	count := 0
	for rows.Next() {
		var username, role, note string
		var isActive bool
		var createdAt time.Time
		rows.Scan(&username, &role, &note, &isActive, &createdAt)

		activeStr := "yes"
		if !isActive {
			activeStr = "no"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			username, role, note, activeStr, createdAt.Format("2006-01-02"),
		)
		count++
	}
	w.Flush()

	if count == 0 {
		fmt.Println("No trusted users found.")
	}

	return nil
}

// runInfo shows database info
func runInfo() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var roleCount, keyCount, permCount, userCount int
	var activeKeyCount, activeUserCount int

	db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roleCount)
	db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&keyCount)
	db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE is_active = true").Scan(&activeKeyCount)
	db.QueryRow("SELECT COUNT(*) FROM permissions").Scan(&permCount)
	// trusted_users table may not exist in older databases
	db.QueryRow("SELECT COUNT(*) FROM trusted_users").Scan(&userCount)
	db.QueryRow("SELECT COUNT(*) FROM trusted_users WHERE is_active = true").Scan(&activeUserCount)

	fmt.Printf("Auth Database: %s\n", dbPath)
	fmt.Println()
	fmt.Printf("Statistics:\n")
	fmt.Printf("  Roles:            %d\n", roleCount)
	fmt.Printf("  API Keys:         %d (%d active)\n", keyCount, activeKeyCount)
	fmt.Printf("  Permissions:      %d\n", permCount)
	fmt.Printf("  Trusted Users:    %d (%d active)\n", userCount, activeUserCount)

	return nil
}
