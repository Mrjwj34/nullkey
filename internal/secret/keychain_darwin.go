//go:build darwin && cgo

package secret

// This file is the macOS production backend. It talks to Security.framework
// directly through cgo: no `security` subprocess, no argv, no shell. Secrets
// travel as CFData and are copied into C memory only for the duration of the
// framework call.
//
// The C wrapper returns only integers and pointers to C-allocated memory, so
// Go never passes a pointer to Go memory containing pointers across the cgo
// boundary.

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation

#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

// nk_cfstring_to_c copies a CFString into a malloc'd UTF-8 C string. The caller
// owns the result and must free(3) it.
static char *nk_cfstring_to_c(CFStringRef s) {
	if (s == NULL) {
		return NULL;
	}
	CFIndex length = CFStringGetLength(s);
	CFIndex capacity = CFStringGetMaximumSizeForEncoding(length, kCFStringEncodingUTF8) + 1;
	if (capacity <= 0) {
		return NULL;
	}
	char *buf = (char *)malloc((size_t)capacity);
	if (buf == NULL) {
		return NULL;
	}
	if (!CFStringGetCString(s, buf, capacity, kCFStringEncodingUTF8)) {
		free(buf);
		return NULL;
	}
	return buf;
}

// nk_copy_error_message returns a malloc'd human-readable message for status,
// or NULL when unavailable. It does not touch any Keychain item.
static char *nk_copy_error_message(OSStatus status) {
	CFStringRef message = SecCopyErrorMessageString(status, NULL);
	if (message == NULL) {
		return NULL;
	}
	char *buf = nk_cfstring_to_c(message);
	CFRelease(message);
	return buf;
}

static CFMutableDictionaryRef nk_base_query(const char *service, const char *account) {
	if (service == NULL || account == NULL) {
		return NULL;
	}
	CFStringRef svc = CFStringCreateWithCString(kCFAllocatorDefault, service, kCFStringEncodingUTF8);
	CFStringRef acct = CFStringCreateWithCString(kCFAllocatorDefault, account, kCFStringEncodingUTF8);
	if (svc == NULL || acct == NULL) {
		if (svc != NULL) CFRelease(svc);
		if (acct != NULL) CFRelease(acct);
		return NULL;
	}
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(
		kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks,
		&kCFTypeDictionaryValueCallBacks);
	if (query == NULL) {
		CFRelease(svc);
		CFRelease(acct);
		return NULL;
	}
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecAttrService, svc);
	CFDictionarySetValue(query, kSecAttrAccount, acct);
	CFRelease(svc);
	CFRelease(acct);
	return query;
}

static OSStatus nk_secret_put(const char *service, const char *account,
                              const void *bytes, size_t length) {
	if (service == NULL || account == NULL || (bytes == NULL && length > 0)) {
		return errSecParam;
	}
	CFMutableDictionaryRef query = nk_base_query(service, account);
	if (query == NULL) {
		return errSecAllocate;
	}
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)bytes, (CFIndex)length);
	if (data == NULL) {
		CFRelease(query);
		return errSecAllocate;
	}
	CFMutableDictionaryRef add = CFDictionaryCreateMutableCopy(kCFAllocatorDefault, 0, query);
	if (add == NULL) {
		CFRelease(data);
		CFRelease(query);
		return errSecAllocate;
	}
	CFDictionarySetValue(add, kSecValueData, data);

	OSStatus status = errSecSuccess;
	for (int attempt = 0; attempt < 2; attempt++) {
		status = SecItemAdd(add, NULL);
		if (status == errSecSuccess) {
			break;
		}
		if (status != errSecDuplicateItem) {
			break;
		}
		CFMutableDictionaryRef attrs = CFDictionaryCreateMutable(
			kCFAllocatorDefault, 0,
			&kCFTypeDictionaryKeyCallBacks,
			&kCFTypeDictionaryValueCallBacks);
		if (attrs == NULL) {
			status = errSecAllocate;
			break;
		}
		CFDictionarySetValue(attrs, kSecValueData, data);
		status = SecItemUpdate(query, attrs);
		CFRelease(attrs);
		// If the item vanished between add and update, loop and try add again.
		if (status != errSecItemNotFound) {
			break;
		}
	}
	CFRelease(add);
	CFRelease(data);
	CFRelease(query);
	return status;
}

typedef struct nk_secret_get_result {
	OSStatus status;
	void *data;
	size_t length;
} nk_secret_get_result;

static nk_secret_get_result nk_secret_get(const char *service, const char *account) {
	nk_secret_get_result result;
	result.status = errSecSuccess;
	result.data = NULL;
	result.length = 0;

	if (service == NULL || account == NULL) {
		result.status = errSecParam;
		return result;
	}
	CFMutableDictionaryRef query = nk_base_query(service, account);
	if (query == NULL) {
		result.status = errSecAllocate;
		return result;
	}
	CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);

	CFTypeRef found = NULL;
	result.status = SecItemCopyMatching(query, &found);
	CFRelease(query);
	if (result.status != errSecSuccess) {
		return result;
	}
	if (found == NULL || CFGetTypeID(found) != CFDataGetTypeID()) {
		if (found != NULL) {
			CFRelease(found);
		}
		result.status = errSecDecode;
		return result;
	}
	CFDataRef data = (CFDataRef)found;
	CFIndex n = CFDataGetLength(data);
	if (n < 0) {
		CFRelease(found);
		result.status = errSecDecode;
		return result;
	}
	size_t length = (size_t)n;
	// malloc(0) may return NULL; allocate one byte so the success path always
	// has a valid pointer, but report length 0.
	void *buf = malloc(length > 0 ? length : 1);
	if (buf == NULL) {
		CFRelease(found);
		result.status = errSecAllocate;
		return result;
	}
	if (length > 0) {
		memcpy(buf, CFDataGetBytePtr(data), length);
	}
	CFRelease(found);
	result.data = buf;
	result.length = length;
	return result;
}

static OSStatus nk_secret_delete(const char *service, const char *account) {
	if (service == NULL || account == NULL) {
		return errSecParam;
	}
	CFMutableDictionaryRef query = nk_base_query(service, account);
	if (query == NULL) {
		return errSecAllocate;
	}
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"
)

const keychainService = "com.nullkey.credentials"

// keychainOps is injectable so unit tests can exercise validation and error
// propagation without ever touching the user's login Keychain.
type keychainOps struct {
	put    func(account string, value []byte) error
	get    func(account string) ([]byte, error)
	delete func(account string) error
}

type keychainStore struct {
	ops keychainOps
}

func newKeychainStore() *keychainStore {
	return &keychainStore{ops: keychainOps{
		put:    keychainPut,
		get:    keychainGet,
		delete: keychainDelete,
	}}
}

func (s *keychainStore) Put(ctx context.Context, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAccount(account); err != nil {
		return err
	}
	return s.ops.put(account, value)
}

func (s *keychainStore) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAccount(account); err != nil {
		return nil, err
	}
	value, err := s.ops.get(account)
	if err != nil {
		return nil, err
	}
	// Match the other backends: the caller owns the returned slice.
	return append([]byte(nil), value...), nil
}

func (s *keychainStore) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAccount(account); err != nil {
		return err
	}
	return s.ops.delete(account)
}

// keychainPut stores value as CFData. The value is never placed in argv or in
// an environment variable.
func keychainPut(account string, value []byte) error {
	cService := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(account)
	defer C.free(unsafe.Pointer(cAccount))

	// Convert an empty value into a one-byte C allocation with length zero so
	// CFDataCreate never receives a NULL bytes pointer.
	data := C.CBytes([]byte{0})
	if len(value) > 0 {
		C.free(data)
		data = C.CBytes(value)
	}
	defer C.free(data)

	status := C.nk_secret_put(cService, cAccount, data, C.size_t(len(value)))
	if status != C.errSecSuccess {
		return keychainError("put", status)
	}
	return nil
}

func keychainGet(account string) ([]byte, error) {
	cService := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(account)
	defer C.free(unsafe.Pointer(cAccount))

	result := C.nk_secret_get(cService, cAccount)
	if result.status == C.errSecItemNotFound {
		return nil, fmt.Errorf("%w: account not found", ErrNotFound)
	}
	if result.status != C.errSecSuccess {
		return nil, keychainError("get", result.status)
	}
	if result.data == nil {
		return nil, fmt.Errorf("secret: keychain returned no data")
	}
	defer C.free(result.data)
	length := int(result.length)
	if length == 0 {
		return []byte{}, nil
	}
	out := make([]byte, length)
	copy(out, unsafe.Slice((*byte)(result.data), length))
	return out, nil
}

func keychainDelete(account string) error {
	cService := C.CString(keychainService)
	defer C.free(unsafe.Pointer(cService))
	cAccount := C.CString(account)
	defer C.free(unsafe.Pointer(cAccount))

	status := C.nk_secret_delete(cService, cAccount)
	// Deleting an absent item is an idempotent success, matching the other
	// backends.
	if status == C.errSecSuccess || status == C.errSecItemNotFound {
		return nil
	}
	return keychainError("delete", status)
}

// keychainError converts an OSStatus into an error without ever including the
// secret value. The CoreFoundation message lookup does not read Keychain items.
func keychainError(op string, status C.OSStatus) error {
	var message string
	if cMessage := C.nk_copy_error_message(status); cMessage != nil {
		message = C.GoString(cMessage)
		C.free(unsafe.Pointer(cMessage))
	}
	if message == "" {
		return fmt.Errorf("secret: keychain %s failed: OSStatus %d", op, int32(status))
	}
	return fmt.Errorf("secret: keychain %s failed: %s (OSStatus %d)", op, message, int32(status))
}

var _ Store = (*keychainStore)(nil)
