# User Service Example

A complete example showing how to use grpcq with the same service implementation for both gRPC and queue modes.

## Quick Start

```bash
# Run demo (async server + client with in-memory queue)
go run main.go

# Run specific modes
go run main.go -mode sync-server   # Traditional gRPC server
go run main.go -mode sync-client   # Traditional gRPC client
go run main.go -mode async-server  # Queue consumer
go run main.go -mode async-client  # Queue producer
```

## Service Implementation

The service implementation (`service.go`) works for both modes:

```go
type UserService struct {
    userpb.UnimplementedUserServiceServer
    users map[string]*User
}

func (s *UserService) CreateUser(ctx context.Context, req *userpb.CreateUserRequest) (*userpb.CreateUserResponse, error) {
    // Same implementation for both sync and async
    user := &User{
        Id:    generateID(),
        Name:  req.Name,
        Email: req.Email,
    }
    s.users[user.Id] = user
    return &userpb.CreateUserResponse{
        UserId: user.Id,
        Name:   user.Name,
        Email:  user.Email,
    }, nil
}
```

## Running Modes

### Demo Mode (Default)
Runs both async server and client with an in-memory queue:
```bash
go run main.go
```

### Synchronous (Traditional gRPC)
```bash
# Terminal 1: Start server
go run main.go -mode sync-server

# Terminal 2: Run client
go run main.go -mode sync-client
```

### Asynchronous (Queue-based)
```bash
# Terminal 1: Start consumer
go run main.go -mode async-server

# Terminal 2: Send messages
go run main.go -mode async-client
```

## Custom Queue Endpoints

Override the default in-memory queue endpoints:
```bash
# Server with custom listen address
go run main.go -mode async-server -queue_listen 0.0.0.0:9090

# Client with custom endpoint
go run main.go -mode async-client -queue_endpoint http://localhost:9090
```
