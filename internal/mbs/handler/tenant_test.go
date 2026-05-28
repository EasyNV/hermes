package handler

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTenantFromCtx_EmptyWhenAbsent(t *testing.T) {
	if got := tenantFromCtx(context.Background()); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestRequireTenant_Missing(t *testing.T) {
	_, err := requireTenant(context.Background())
	if err == nil {
		t.Fatal("expected Unauthenticated error")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("code: got %v want Unauthenticated", status.Code(err))
	}
}

func TestRequireTenant_Present(t *testing.T) {
	ctx := withTenantForTest(context.Background(), "tenant-A")
	got, err := requireTenant(ctx)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != "tenant-A" {
		t.Errorf("got %q want tenant-A", got)
	}
}

func TestInjectTenant_FromMetadata(t *testing.T) {
	md := metadata.New(map[string]string{TenantMetadataKey: "tenant-X"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	ctx, err := injectTenant(ctx)
	if err != nil {
		t.Fatalf("injectTenant: %v", err)
	}
	if got := tenantFromCtx(ctx); got != "tenant-X" {
		t.Errorf("got %q want tenant-X", got)
	}
}

func TestInjectTenant_NoMetadata(t *testing.T) {
	_, err := injectTenant(context.Background())
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestInjectTenant_EmptyValue(t *testing.T) {
	md := metadata.New(map[string]string{TenantMetadataKey: ""})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := injectTenant(ctx)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated on empty value, got %v", err)
	}
}
