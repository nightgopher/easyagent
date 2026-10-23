//go:build darwin

package credentialstore

import (
	"bytes"
	"encoding/hex"
	"errors"
	"unsafe"

	"github.com/ebitengine/purego"
)

const AppID = "com.hwj.easyagent"
const HelperID = AppID + ".credentials"

// Identity returns the validated designated requirement and CodeDirectory hash.
// A path selects static code; an empty path selects this process.
func Identity(path string) (string, string, error) {
	if err := initKeychain(); err != nil {
		return "", "", err
	}
	var self func(uint32, *uintptr) int32
	var static func(uintptr, uint32, *uintptr) int32
	var urlNew func(uintptr, *byte, int64, bool) uintptr
	var copyRequirement func(uintptr, uint32, *uintptr) int32
	var copyInfo func(uintptr, uint32, *uintptr) int32
	var requirementString func(uintptr, uint32, *uintptr) int32
	var get func(uintptr, uintptr) uintptr
	var stringBytes func(uintptr, *byte, int64, uint32) bool
	purego.RegisterLibFunc(&self, keychain.security, "SecCodeCopySelf")
	purego.RegisterLibFunc(&static, keychain.security, "SecStaticCodeCreateWithPath")
	purego.RegisterLibFunc(&urlNew, keychain.core, "CFURLCreateFromFileSystemRepresentation")
	purego.RegisterLibFunc(&copyRequirement, keychain.security, "SecCodeCopyDesignatedRequirement")
	purego.RegisterLibFunc(&copyInfo, keychain.security, "SecCodeCopySigningInformation")
	purego.RegisterLibFunc(&requirementString, keychain.security, "SecRequirementCopyString")
	purego.RegisterLibFunc(&get, keychain.core, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&stringBytes, keychain.core, "CFStringGetCString")
	var code uintptr
	var status int32
	if path == "" {
		status = self(0, &code)
	} else {
		data := []byte(path)
		url := urlNew(0, unsafe.SliceData(data), int64(len(data)), false)
		defer keychain.release(url)
		status = static(url, 0, &code)
	}
	if status != 0 || code == 0 {
		return "", "", errors.New("无法读取凭据程序的代码身份")
	}
	defer keychain.release(code)
	var requirement, text, info uintptr
	if copyRequirement(code, 0, &requirement) != 0 || requirement == 0 {
		return "", "", errors.New("凭据程序必须使用有效代码签名")
	}
	defer keychain.release(requirement)
	if requirementString(requirement, 0, &text) != 0 || text == 0 {
		return "", "", errors.New("无法读取凭据程序的签名要求")
	}
	defer keychain.release(text)
	buffer := make([]byte, 65536)
	if !stringBytes(text, &buffer[0], int64(len(buffer)), 0x08000100) {
		return "", "", errors.New("凭据程序的签名要求无效")
	}
	if copyInfo(code, 2, &info) != 0 || info == 0 { // kSecCSSigningInformation
		return "", "", errors.New("无法读取凭据程序的代码哈希")
	}
	defer keychain.release(info)
	hashData := get(info, keychainConstant("kSecCodeInfoUnique"))
	if hashData == 0 || keychain.dataLength(hashData) != 20 {
		return "", "", errors.New("凭据程序的代码哈希无效")
	}
	hash := unsafe.Slice((*byte)(nativePointer(keychain.dataBytes(hashData))), 20)
	return string(buffer[:bytes.IndexByte(buffer, 0)]), hex.EncodeToString(hash), nil
}

// Verify authenticates an on-disk helper or a live parent process. pid == 0
// selects the file path. Credentials are never used to establish code identity.
func Verify(path string, pid int, requirement string) error {
	if err := initKeychain(); err != nil {
		return err
	}
	var createRequirement func(uintptr, uint32, *uintptr) int32
	purego.RegisterLibFunc(&createRequirement, keychain.security, "SecRequirementCreateWithString")
	text := keychain.stringNew(0, requirement, 0x08000100)
	defer keychain.release(text)
	var req, code uintptr
	if createRequirement(text, 0, &req) != 0 || req == 0 {
		return errors.New("凭据程序的签名要求无效")
	}
	defer keychain.release(req)
	var status int32
	if pid != 0 {
		var numberNew func(uintptr, int64, *int32) uintptr
		var guest func(uintptr, uintptr, uint32, *uintptr) int32
		var check func(uintptr, uint32, uintptr) int32
		purego.RegisterLibFunc(&numberNew, keychain.core, "CFNumberCreate")
		purego.RegisterLibFunc(&guest, keychain.security, "SecCodeCopyGuestWithAttributes")
		purego.RegisterLibFunc(&check, keychain.security, "SecCodeCheckValidity")
		parent := int32(pid)
		number := numberNew(0, 3, &parent) // kCFNumberSInt32Type
		defer keychain.release(number)
		attrs := keychainDictionary([]uintptr{keychainConstant("kSecGuestAttributePid")}, []uintptr{number})
		defer keychain.release(attrs)
		status = guest(0, attrs, 0, &code)
		if status == 0 && code != 0 {
			defer keychain.release(code)
			status = check(code, 0, req)
		}
	} else {
		var urlNew func(uintptr, *byte, int64, bool) uintptr
		var create func(uintptr, uint32, *uintptr) int32
		var check func(uintptr, uint32, uintptr) int32
		purego.RegisterLibFunc(&urlNew, keychain.core, "CFURLCreateFromFileSystemRepresentation")
		purego.RegisterLibFunc(&create, keychain.security, "SecStaticCodeCreateWithPath")
		purego.RegisterLibFunc(&check, keychain.security, "SecStaticCodeCheckValidity")
		data := []byte(path)
		url := urlNew(0, unsafe.SliceData(data), int64(len(data)), false)
		defer keychain.release(url)
		status = create(url, 0, &code)
		if status == 0 && code != 0 {
			defer keychain.release(code)
			status = check(code, 0, req)
		}
	}
	if status != 0 || code == 0 {
		return errors.New("凭据访问被拒绝：应用签名不匹配")
	}
	return nil
}

func DisableInteraction() error {
	if err := initKeychain(); err != nil {
		return err
	}
	var allow func(bool) int32
	purego.RegisterLibFunc(&allow, keychain.security, "SecKeychainSetUserInteractionAllowed")
	if allow(false) != 0 {
		return errors.New("无法禁用凭据程序的系统密码弹窗")
	}
	return nil
}
