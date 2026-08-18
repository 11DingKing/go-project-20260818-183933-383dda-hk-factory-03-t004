package store

import (
	"context"
	"database/sql"

	"sitesync/internal/domain"
)

const personCols = `id, code, name, role, email, active, version, created_at, updated_at`

// CreatePerson inserts a staff member. Duplicate code yields AlreadyExists.
func (s *Store) CreatePerson(ctx context.Context, p domain.Person) (domain.Person, error) {
	p.Version = 1
	p.CreatedAt = s.clock.Now()
	p.UpdatedAt = p.CreatedAt
	now := s.nowRFC3339()
	res, err := s.txFrom(ctx).ExecContext(ctx, personInsert, p.ID, p.Code, p.Name, p.Role, p.Email,
		intValue(p.Active), p.Version, now, now)
	return p, dupInsert(res, err, "person", p.Code)
}

const personInsert = `INSERT INTO persons (id, code, name, role, email, active, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

func scanPerson(sc rowScanner) (domain.Person, error) {
	var p domain.Person
	var active int
	var created, updated sql.NullString
	if err := sc.Scan(&p.ID, &p.Code, &p.Name, &p.Role, &p.Email, &active, &p.Version, &created, &updated); err != nil {
		return p, err
	}
	p.Active = active == 1
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

// GetPerson loads a person by id.
func (s *Store) GetPerson(ctx context.Context, id string) (domain.Person, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+personCols+` FROM persons WHERE id = ?`, scanPerson, "store: get person "+id, id)
}

// CreateCustomer inserts a workshop customer.
func (s *Store) CreateCustomer(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	c.Version = 1
	c.CreatedAt = s.clock.Now()
	c.UpdatedAt = c.CreatedAt
	now := s.nowRFC3339()
	res, err := s.txFrom(ctx).ExecContext(ctx, customerInsert, c.ID, c.Code, c.Name, c.Workshop,
		c.Contact, c.Status, c.Version, now, now)
	return c, dupInsert(res, err, "customer", c.Code)
}

const customerInsert = `INSERT INTO customers (id, code, name, workshop, contact, status, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const customerCols = `id, code, name, workshop, contact, status, version, created_at, updated_at`

func scanCustomer(sc rowScanner) (domain.Customer, error) {
	var c domain.Customer
	var created, updated sql.NullString
	if err := sc.Scan(&c.ID, &c.Code, &c.Name, &c.Workshop, &c.Contact, &c.Status, &c.Version, &created, &updated); err != nil {
		return c, err
	}
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return c, nil
}

// GetCustomer loads a customer by id.
func (s *Store) GetCustomer(ctx context.Context, id string) (domain.Customer, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+customerCols+` FROM customers WHERE id = ?`, scanCustomer, "store: get customer "+id, id)
}

// CreateDevice inserts an equipment unit bound to a customer.
func (s *Store) CreateDevice(ctx context.Context, d domain.Device) (domain.Device, error) {
	d.Version = 1
	d.CreatedAt = s.clock.Now()
	d.UpdatedAt = d.CreatedAt
	now := s.nowRFC3339()
	res, err := s.txFrom(ctx).ExecContext(ctx, deviceInsert, d.ID, d.CustomerID, d.Serial, d.Name,
		d.Model, d.Status, d.Version, now, now)
	return d, dupInsert(res, err, "device", d.Serial)
}

const deviceInsert = `INSERT INTO devices (id, customer_id, serial, name, model, status, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const deviceCols = `id, customer_id, serial, name, model, status, version, created_at, updated_at`

func scanDevice(sc rowScanner) (domain.Device, error) {
	var d domain.Device
	var created, updated sql.NullString
	if err := sc.Scan(&d.ID, &d.CustomerID, &d.Serial, &d.Name, &d.Model, &d.Status, &d.Version, &created, &updated); err != nil {
		return d, err
	}
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return d, nil
}

// GetDevice loads a device by id.
func (s *Store) GetDevice(ctx context.Context, id string) (domain.Device, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+deviceCols+` FROM devices WHERE id = ?`, scanDevice, "store: get device "+id, id)
}
