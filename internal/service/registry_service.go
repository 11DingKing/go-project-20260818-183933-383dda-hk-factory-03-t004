package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// RegistryService creates the master-data entities a deployment needs.
type RegistryService struct {
	deps Deps
}

// CreatePersonRequest is the wire input for registering staff.
type CreatePersonRequest struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	Email string `json:"email"`
}

// CreatePerson registers a field engineer, customer manager or adjudicator.
func (s *RegistryService) CreatePerson(ctx context.Context, actor string, req CreatePersonRequest) (domain.Person, error) {
	if err := validatePerson(req); err != nil {
		return domain.Person{}, err
	}
	p := domain.Person{
		ID: uuid.NewString(), Code: req.Code, Name: req.Name, Role: req.Role,
		Email: req.Email, Active: true,
	}
	created, err := s.deps.Persons.CreatePerson(ctx, p)
	if err != nil {
		return domain.Person{}, err
	}
	s.deps.audit(ctx, actor, req.Role, "person.create", "person", created.ID, "code="+created.Code)
	return created, nil
}

func validatePerson(req CreatePersonRequest) error {
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Role) == "" {
		return fmt.Errorf("%w: code, name and role are required", errorsx.ErrValidation)
	}
	switch req.Role {
	case "field_engineer", "customer_manager", "adjudicator", "ops_specialist":
		return nil
	default:
		return fmt.Errorf("%w: unknown role %q", errorsx.ErrValidation, req.Role)
	}
}

// CreateCustomerRequest is the wire input for registering a workshop.
type CreateCustomerRequest struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Workshop string `json:"workshop"`
	Contact  string `json:"contact"`
}

// CreateCustomer registers a customer workshop.
func (s *RegistryService) CreateCustomer(ctx context.Context, actor string, req CreateCustomerRequest) (domain.Customer, error) {
	if strings.TrimSpace(req.Code) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Workshop) == "" {
		return domain.Customer{}, fmt.Errorf("%w: code, name and workshop are required", errorsx.ErrValidation)
	}
	c := domain.Customer{
		ID: uuid.NewString(), Code: req.Code, Name: req.Name, Workshop: req.Workshop, Contact: req.Contact, Status: "active",
	}
	created, err := s.deps.Customers.CreateCustomer(ctx, c)
	if err != nil {
		return domain.Customer{}, err
	}
	s.deps.audit(ctx, actor, "ops_specialist", "customer.create", "customer", created.ID, "code="+created.Code)
	return created, nil
}

// CreateDeviceRequest is the wire input for registering equipment.
type CreateDeviceRequest struct {
	CustomerID string `json:"customer_id"`
	Serial     string `json:"serial"`
	Name       string `json:"name"`
	Model      string `json:"model"`
}

// CreateDevice registers a unit of equipment under a customer.
func (s *RegistryService) CreateDevice(ctx context.Context, actor string, req CreateDeviceRequest) (domain.Device, error) {
	if strings.TrimSpace(req.CustomerID) == "" || strings.TrimSpace(req.Serial) == "" || strings.TrimSpace(req.Name) == "" {
		return domain.Device{}, fmt.Errorf("%w: customer_id, serial and name are required", errorsx.ErrValidation)
	}
	if _, err := s.deps.Customers.GetCustomer(ctx, req.CustomerID); err != nil {
		return domain.Device{}, fmt.Errorf("create device: %w", err)
	}
	d := domain.Device{
		ID: uuid.NewString(), CustomerID: req.CustomerID, Serial: req.Serial, Name: req.Name, Model: req.Model, Status: "idle",
	}
	created, err := s.deps.Devices.CreateDevice(ctx, d)
	if err != nil {
		return domain.Device{}, err
	}
	s.deps.audit(ctx, actor, "ops_specialist", "device.create", "device", created.ID, "serial="+created.Serial)
	return created, nil
}
