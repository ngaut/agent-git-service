package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"gh-server/internal/authn"
	"gh-server/internal/crypto"
	"gh-server/internal/db"
)

// RouterConfig holds per-tenant connection pool settings and cache limits.
type RouterConfig struct {
	MaxOpenConns    int           // per-tenant; default 5
	MaxIdleConns    int           // per-tenant; default 2
	ConnMaxLifetime time.Duration // per-tenant; default 30m
	MaxAgents       int           // cache cap; default 100
}

func (c RouterConfig) withDefaults() RouterConfig {
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 5
	}
	if c.MaxIdleConns <= 0 {
		c.MaxIdleConns = 2
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 30 * time.Minute
	}
	if c.MaxAgents <= 0 {
		c.MaxAgents = 100
	}
	return c
}

// OpenDBFunc opens a *gorm.DB for a given DSN. Production uses mysql.Open;
// tests can inject SQLite or other dialects.
type OpenDBFunc func(dsn string) (*gorm.DB, error)

// DBRouter resolves tokens to tenant databases via the control plane.
type DBRouter struct {
	cpDB            *gorm.DB
	openDB          OpenDBFunc
	multiTenantMode bool
	cache           map[uint]*gorm.DB // CPUser.ID → cached tenant DB
	pending         int               // in-flight opens not yet in cache
	mu              sync.RWMutex
	flights         singleflight.Group
	cfg             RouterConfig
}

// Note: controlplane used to define ErrUnknownToken and ErrInactiveUser here,
// but these have been moved to internal/authn to avoid layer violations.
// This package now wraps authn errors for backward compatibility.

// NewDBRouter creates a DBRouter backed by the control plane database.
// openDB is called to open tenant DB connections (allows test injection).
func NewDBRouter(cpDB *gorm.DB, openDB OpenDBFunc, multiTenantMode bool, cfg RouterConfig) *DBRouter {
	return &DBRouter{
		cpDB:            cpDB,
		openDB:          openDB,
		multiTenantMode: multiTenantMode,
		cache:           make(map[uint]*gorm.DB),
		cfg:             cfg.withDefaults(),
	}
}

// ResolveToken looks up the token in the control plane, returns the
// tenant-scoped db.User and the tenant *gorm.DB handle.
func (r *DBRouter) ResolveToken(ctx context.Context, token string) (db.User, *gorm.DB, error) {
	if r == nil || r.cpDB == nil || r.openDB == nil {
		return db.User{}, nil, errors.New("controlplane: db router is not initialized")
	}

	// Step 1: look up token → CPUser in control plane
	var cpToken CPToken
	if err := r.cpDB.WithContext(ctx).Preload("CPUser").Where("value = ?", token).First(&cpToken).Error; err != nil {
		return db.User{}, nil, fmt.Errorf("%w: %v", authn.ErrUnknownToken, err)
	}
	cpUser := cpToken.CPUser

	if r.multiTenantMode && cpUser.State != AgentStateActive {
		return db.User{}, nil, fmt.Errorf("%w: agent state %s is not active", authn.ErrInactiveUser, cpUser.State)
	}

	// Step 2: get or create tenant DB connection
	tenantDB, err := r.getOrOpenDB(ctx, cpUser)
	if err != nil {
		return db.User{}, nil, err
	}

	// Step 3: look up or create tenant-scoped db.User
	tenantUser, err := r.ensureTenantUser(ctx, tenantDB, cpUser)
	if err != nil {
		return db.User{}, nil, fmt.Errorf("controlplane: tenant user: %w", err)
	}

	return tenantUser, tenantDB, nil
}

// getOrOpenDB returns a cached tenant DB or opens a new one (serialized per agent).
func (r *DBRouter) getOrOpenDB(ctx context.Context, cpUser CPUser) (*gorm.DB, error) {
	// Fast path: read-lock cache check
	r.mu.RLock()
	if tdb, ok := r.cache[cpUser.ID]; ok {
		r.mu.RUnlock()
		return tdb, nil
	}
	r.mu.RUnlock()

	// Slow path: singleflight ensures one connection+migration per agent
	key := fmt.Sprint(cpUser.ID)
	val, err, _ := r.flights.Do(key, func() (any, error) {
		// Reserve capacity atomically: check cache, pending, and cap under one lock.
		r.mu.Lock()
		if tdb, ok := r.cache[cpUser.ID]; ok {
			r.mu.Unlock()
			return tdb, nil
		}
		if len(r.cache)+r.pending >= r.cfg.MaxAgents {
			r.mu.Unlock()
			return nil, errors.New("controlplane: max agents capacity reached")
		}
		r.pending++
		r.mu.Unlock()

		// Open tenant DB (slow; runs outside lock)
		tdb, err := r.openDB(cpUser.DSN)
		if err != nil {
			r.mu.Lock()
			r.pending--
			r.mu.Unlock()
			return nil, fmt.Errorf("controlplane: open tenant db: %w", err)
		}

		// Configure pool
		sqlDB, err := tdb.DB()
		if err != nil {
			r.mu.Lock()
			r.pending--
			r.mu.Unlock()
			return nil, fmt.Errorf("controlplane: get sql.DB: %w", err)
		}
		sqlDB.SetMaxOpenConns(r.cfg.MaxOpenConns)
		sqlDB.SetMaxIdleConns(r.cfg.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(r.cfg.ConnMaxLifetime)

		// Run migration on tenant DB
		if err := db.Migrate(tdb); err != nil {
			sqlDB.Close()
			r.mu.Lock()
			r.pending--
			r.mu.Unlock()
			return nil, fmt.Errorf("controlplane: migrate tenant db: %w", err)
		}

		// Commit to cache: convert reservation into cached entry
		r.mu.Lock()
		r.cache[cpUser.ID] = tdb
		r.pending--
		r.mu.Unlock()

		return tdb, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*gorm.DB), nil
}

// ensureTenantUser looks up or creates a db.User in the tenant DB matching
// the control plane user's login/email. This ensures GetCurrentUser(ctx) works.
func (r *DBRouter) ensureTenantUser(_ context.Context, tenantDB *gorm.DB, cpUser CPUser) (db.User, error) {
	var u db.User
	err := tenantDB.Where("login = ?", cpUser.Login).First(&u).Error
	if err == nil {
		if u.UserKind != db.UserKindAgent || !u.SiteAdmin {
			updates := map[string]any{}
			if u.UserKind != db.UserKindAgent {
				updates["user_kind"] = db.UserKindAgent
				u.UserKind = db.UserKindAgent
			}
			if !u.SiteAdmin {
				updates["site_admin"] = true
				u.SiteAdmin = true
			}
			if len(updates) > 0 {
				if err := tenantDB.Model(&u).Updates(updates).Error; err != nil {
					return db.User{}, err
				}
			}
		}
		return u, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return db.User{}, err
	}
	// Create the user in tenant DB
	u = db.User{
		Login:     cpUser.Login,
		Name:      cpUser.Login,
		Email:     cpUser.Email,
		Type:      db.TypeUser,
		UserKind:  db.UserKindAgent,
		SiteAdmin: true,
	}
	if err := tenantDB.Create(&u).Error; err != nil {
		// Race: another request may have created it
		if tenantDB.Where("login = ?", cpUser.Login).First(&u).Error == nil {
			return u, nil
		}
		return db.User{}, err
	}
	return u, nil
}

// PingCP pings the control-plane database to verify connectivity.
func (r *DBRouter) PingCP(ctx context.Context) error {
	sqlDB, err := r.cpDB.DB()
	if err != nil {
		return fmt.Errorf("controlplane: get sql.DB: %w", err)
	}
	return sqlDB.PingContext(ctx)
}

// TenantDBs returns tenant databases for all active control-plane users.
func (r *DBRouter) TenantDBs(ctx context.Context) ([]*gorm.DB, error) {
	if r == nil || r.cpDB == nil || r.openDB == nil {
		return nil, errors.New("controlplane: db router is not initialized")
	}

	var users []CPUser
	if err := r.cpDB.WithContext(ctx).Where("state = ?", AgentStateActive).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("controlplane: list active users: %w", err)
	}

	dbs := make([]*gorm.DB, 0, len(users))
	for _, user := range users {
		tenantDB, err := r.getOrOpenDB(ctx, user)
		if err != nil {
			return nil, fmt.Errorf("controlplane: open tenant db for %s: %w", user.Login, err)
		}
		dbs = append(dbs, tenantDB)
	}
	return dbs, nil
}

// Close drains all cached tenant DB connections.
func (r *DBRouter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for id, tdb := range r.cache {
		if sqlDB, err := tdb.DB(); err == nil {
			if err := sqlDB.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(r.cache, id)
	}
	return firstErr
}

// CreateUser creates a new control plane user initialized in pending state.
func (r *DBRouter) CreateUser(ctx context.Context, login, email string) (*CPUser, error) {
	placeholderDSN, err := crypto.EncryptSecret("")
	if err != nil {
		return nil, fmt.Errorf("controlplane: encrypt placeholder DSN: %w", err)
	}

	cpUser := &CPUser{
		Login: login,
		Email: email,
		DSN:   placeholderDSN,
		State: AgentStatePending,
	}

	if err := r.cpDB.WithContext(ctx).Create(cpUser).Error; err != nil {
		return nil, fmt.Errorf("controlplane: create user: %w", err)
	}

	return cpUser, nil
}

// CreateToken creates a new token for a control plane user.
func (r *DBRouter) CreateToken(ctx context.Context, cpUserID uint, tokenValue string) error {
	token := &CPToken{
		CPUserID: cpUserID,
		Value:    tokenValue,
	}
	if err := r.cpDB.WithContext(ctx).Create(token).Error; err != nil {
		return fmt.Errorf("controlplane: create token: %w", err)
	}
	return nil
}

// GetUserByLogin returns a CPUser by login.
func (r *DBRouter) GetUserByLogin(ctx context.Context, login string) (*CPUser, error) {
	var cpUser CPUser
	if err := r.cpDB.WithContext(ctx).First(&cpUser, "login = ?", login).Error; err != nil {
		return nil, fmt.Errorf("controlplane: get user: %w", err)
	}
	return &cpUser, nil
}

// DeleteUser deletes a control plane user and associated tokens.
func (r *DBRouter) DeleteUser(ctx context.Context, cpUserID uint) error {
	// Delete tokens first (foreign key constraint)
	if err := r.cpDB.WithContext(ctx).Where(&CPToken{CPUserID: cpUserID}).Delete(&CPToken{}).Error; err != nil {
		return fmt.Errorf("controlplane: delete tokens: %w", err)
	}
	// Delete user
	if err := r.cpDB.WithContext(ctx).Delete(&CPUser{}, cpUserID).Error; err != nil {
		return fmt.Errorf("controlplane: delete user: %w", err)
	}
	return nil
}

// DeleteToken deletes tokens for a user.
func (r *DBRouter) DeleteToken(ctx context.Context, cpUserID uint) error {
	if err := r.cpDB.WithContext(ctx).Where(&CPToken{CPUserID: cpUserID}).Delete(&CPToken{}).Error; err != nil {
		return fmt.Errorf("controlplane: delete tokens: %w", err)
	}
	return nil
}

// GetUserByID returns a CPUser by ID.
func (r *DBRouter) GetUserByID(ctx context.Context, id uint) (*CPUser, error) {
	var cpUser CPUser
	if err := r.cpDB.WithContext(ctx).First(&cpUser, id).Error; err != nil {
		return nil, fmt.Errorf("controlplane: get user: %w", err)
	}
	return &cpUser, nil
}
