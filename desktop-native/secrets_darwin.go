//go:build darwin

package main

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Keychain access through Apple's C ABI keeps the client free of cgo and shell
// credential arguments. Values never enter JSON preferences or command lines.
type systemSecrets struct{}

var keychain struct {
	once                         sync.Once
	err                          error
	security, core               uintptr
	keyCallbacks, valueCallbacks uintptr
	arrayCallbacks               uintptr
	stringNew                    func(uintptr, string, uint32) uintptr
	dataNew                      func(uintptr, *byte, int64) uintptr
	dataLength                   func(uintptr) int64
	dataBytes                    func(uintptr) uintptr
	dictionary                   func(uintptr, *uintptr, *uintptr, int64, uintptr, uintptr) uintptr
	array                        func(uintptr, *uintptr, int64, uintptr) uintptr
	defaultKeychain              func(*uintptr) int32
	release                      func(uintptr)
	copy                         func(uintptr, *uintptr) int32
	add                          func(uintptr, uintptr) int32
	update                       func(uintptr, uintptr) int32
	delete                       func(uintptr) int32
}

func initKeychain() error {
	keychain.once.Do(func() {
		k := &keychain
		k.security, k.err = purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if k.err != nil {
			return
		}
		k.core, k.err = purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if k.err != nil {
			return
		}
		purego.RegisterLibFunc(&k.stringNew, k.core, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&k.dataNew, k.core, "CFDataCreate")
		purego.RegisterLibFunc(&k.dataLength, k.core, "CFDataGetLength")
		purego.RegisterLibFunc(&k.dataBytes, k.core, "CFDataGetBytePtr")
		purego.RegisterLibFunc(&k.dictionary, k.core, "CFDictionaryCreate")
		purego.RegisterLibFunc(&k.release, k.core, "CFRelease")
		purego.RegisterLibFunc(&k.copy, k.security, "SecItemCopyMatching")
		purego.RegisterLibFunc(&k.add, k.security, "SecItemAdd")
		purego.RegisterLibFunc(&k.update, k.security, "SecItemUpdate")
		purego.RegisterLibFunc(&k.delete, k.security, "SecItemDelete")
		purego.RegisterLibFunc(&k.array, k.core, "CFArrayCreate")
		purego.RegisterLibFunc(&k.defaultKeychain, k.security, "SecKeychainCopyDefault")
		k.keyCallbacks, k.err = purego.Dlsym(k.core, "kCFTypeDictionaryKeyCallBacks")
		if k.err != nil {
			return
		}
		k.valueCallbacks, k.err = purego.Dlsym(k.core, "kCFTypeDictionaryValueCallBacks")
		if k.err != nil {
			return
		}
		k.arrayCallbacks, k.err = purego.Dlsym(k.core, "kCFTypeArrayCallBacks")
	})
	return keychain.err
}
func keychainConstant(name string) uintptr {
	lib := keychain.security
	if name == "kCFBooleanTrue" {
		lib = keychain.core
	}
	address, err := purego.Dlsym(lib, name)
	if err != nil {
		return 0
	}
	return *(*uintptr)(nativePointer(address))
}
func keychainDictionary(keys, values []uintptr) uintptr {
	return keychain.dictionary(0, &keys[0], &values[0], int64(len(keys)), keychain.keyCallbacks, keychain.valueCallbacks)
}
func keychainQuery(account string, read bool) (uintptr, uintptr, func(), error) {
	// SecItemAdd writes to the default keychain, whereas an unscoped search
	// follows the user's search list. That list may omit the default keychain.
	var target uintptr
	if keychain.defaultKeychain(&target) != 0 || target == 0 {
		return 0, 0, nil, errors.New("无法打开默认系统钥匙串")
	}
	search := keychain.array(0, &target, 1, keychain.arrayCallbacks)
	service := keychain.stringNew(0, "com.hwj.easyagent.native-preview", 0x08000100)
	name := keychain.stringNew(0, account, 0x08000100)
	keys := []uintptr{keychainConstant("kSecClass"), keychainConstant("kSecAttrService"), keychainConstant("kSecAttrAccount"), keychainConstant("kSecMatchSearchList")}
	values := []uintptr{keychainConstant("kSecClassGenericPassword"), service, name, search}
	if read {
		keys = append(keys, keychainConstant("kSecReturnData"), keychainConstant("kSecMatchLimit"))
		values = append(values, keychainConstant("kCFBooleanTrue"), keychainConstant("kSecMatchLimitOne"))
	}
	q := keychainDictionary(keys, values)
	return q, target, func() {
		keychain.release(q)
		keychain.release(service)
		keychain.release(name)
		keychain.release(search)
		keychain.release(target)
	}, nil
}
func (systemSecrets) Get(account string) (string, error) {
	if initKeychain() != nil {
		return "", errors.New("系统钥匙串不可用")
	}
	q, _, done, err := keychainQuery(account, true)
	if err != nil {
		return "", err
	}
	defer done()
	var result uintptr
	status := keychain.copy(q, &result)
	if status == -25300 {
		return "", nil
	}
	if status != 0 || result == 0 {
		return "", errors.New("无法从系统钥匙串读取凭据")
	}
	defer keychain.release(result)
	n := keychain.dataLength(result)
	if n <= 0 || n > 65536 {
		return "", errors.New("钥匙串凭据长度无效")
	}
	p := keychain.dataBytes(result)
	return string(unsafe.Slice((*byte)(nativePointer(p)), int(n))), nil
}
func (systemSecrets) Set(account, value string) error {
	if initKeychain() != nil {
		return errors.New("系统钥匙串不可用，凭据未保存")
	}
	if value == "" {
		return (systemSecrets{}).Delete(account)
	}
	q, target, done, err := keychainQuery(account, false)
	if err != nil {
		return err
	}
	defer done()
	data := []byte(value)
	v := keychain.dataNew(0, &data[0], int64(len(data)))
	defer keychain.release(v)
	k := []uintptr{keychainConstant("kSecValueData")}
	vals := []uintptr{v}
	attrs := keychainDictionary(k, vals)
	defer keychain.release(attrs)
	status := keychain.update(q, attrs)
	if status == -25300 {
		service := keychain.stringNew(0, "com.hwj.easyagent.native-preview", 0x08000100)
		name := keychain.stringNew(0, account, 0x08000100)
		defer keychain.release(service)
		defer keychain.release(name)
		keys := []uintptr{keychainConstant("kSecClass"), keychainConstant("kSecAttrService"), keychainConstant("kSecAttrAccount"), keychainConstant("kSecValueData"), keychainConstant("kSecUseKeychain")}
		values := []uintptr{keychainConstant("kSecClassGenericPassword"), service, name, v, target}
		d := keychainDictionary(keys, values)
		defer keychain.release(d)
		status = keychain.add(d, 0)
	}
	if status != 0 {
		return errors.New("系统钥匙串保存失败，配置未应用")
	}
	return nil
}
func (systemSecrets) Delete(account string) error {
	if initKeychain() != nil {
		return errors.New("系统钥匙串不可用")
	}
	q, _, done, err := keychainQuery(account, false)
	if err != nil {
		return err
	}
	defer done()
	code := keychain.delete(q)
	if code != 0 && code != -25300 {
		return errors.New("无法移除钥匙串凭据")
	}
	return nil
}

// nativePointer is only for addresses returned by Apple APIs, never Go heap addresses.
func nativePointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}
