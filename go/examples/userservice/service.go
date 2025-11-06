package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	userpb "github.com/pbdeuchler/grpcq/go/examples/userservice/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UserService implements both gRPC and grpcq UserServiceServer interfaces.
// This same implementation works for both synchronous gRPC and asynchronous grpcq.
type UserService struct {
	userpb.UnimplementedUserServiceServer // for gRPC compatibility
	mu                                    sync.RWMutex
	users                                 map[string]*userpb.GetUserResponse
}

// NewUserService creates a new UserService instance.
func NewUserService() *UserService {
	return &UserService{
		users: make(map[string]*userpb.GetUserResponse),
	}
}

// CreateUser handles user creation.
// This method works identically whether called via gRPC or grpcq.
func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
	log.Printf("[UserService] CreateUser called: name=%s email=%s", req.Name, req.Email)

	// Validate request
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}

	// Simulate some processing time
	time.Sleep(100 * time.Millisecond)

	// Generate user ID
	userID := fmt.Sprintf("user-%d", time.Now().UnixNano())

	// Store user
	s.mu.Lock()
	s.users[userID] = &userpb.GetUserResponse{
		UserId: userID,
		Name:   req.Name,
		Email:  req.Email,
	}
	s.mu.Unlock()

	log.Printf("[UserService] Successfully created user: id=%s name=%s", userID, req.Name)

	return &userpb.CreateUserResponse{
		UserId: userID,
		Name:   req.Name,
		Email:  req.Email,
	}, nil
}

// GetUser retrieves a user by ID.
// This method works identically whether called via gRPC or grpcq.
func (s *UserService) GetUser(ctx context.Context, req *userpb.GetUserRequest) (*userpb.GetUserResponse, error) {
	log.Printf("[UserService] GetUser called: user_id=%s", req.UserId)

	// Validate request
	if req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	// Retrieve user
	s.mu.RLock()
	user, exists := s.users[req.UserId]
	s.mu.RUnlock()

	if !exists {
		return nil, status.Error(codes.NotFound, "user not found")
	}

	log.Printf("[UserService] Found user: id=%s name=%s", user.UserId, user.Name)

	return user, nil
}
