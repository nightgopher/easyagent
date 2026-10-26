import Foundation
import Security
import EasyAgentBackend

/// 主机 API Key 的存取抽象。真机/模拟器用 Keychain 实现；测试注入内存实现。
public protocol APIKeyStore: Sendable {
    func saveKey(_ key: String, for hostID: UUID) throws
    func key(for hostID: UUID) -> String?
    func removeKey(for hostID: UUID)
}

public enum KeychainError: Error, Equatable {
    case unexpectedStatus(Int32)
}

/// kSecClassGenericPassword：service 固定，account 用 host UUID。
/// Keychain 只在真机/模拟器可用；macOS 上 swift test 用 MemoryKeyStore。
public struct SystemKeychainStore: APIKeyStore {
    private let service: String

    public init(service: String = "com.hwj.easyagent.hostkey") {
        self.service = service
    }

    public func saveKey(_ key: String, for hostID: UUID) throws {
        let data = Data(key.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: hostID.uuidString,
        ]
        let attributes: [String: Any] = [kSecValueData as String: data]
        var status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            var add = query
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            status = SecItemAdd(add as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw KeychainError.unexpectedStatus(status) }
    }

    public func key(for hostID: UUID) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: hostID.uuidString,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var result: AnyObject?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    public func removeKey(for hostID: UUID) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: hostID.uuidString,
        ]
        SecItemDelete(query as CFDictionary)
    }
}

public final class MemoryKeyStore: APIKeyStore, @unchecked Sendable {
    private let lock = NSLock()
    private nonisolated(unsafe) var storage: [UUID: String] = [:]

    public init() {}

    public func saveKey(_ key: String, for hostID: UUID) throws {
        lock.lock(); defer { lock.unlock() }
        storage[hostID] = key
    }

    public func key(for hostID: UUID) -> String? {
        lock.lock(); defer { lock.unlock() }
        return storage[hostID]
    }

    public func removeKey(for hostID: UUID) {
        lock.lock(); defer { lock.unlock() }
        storage[hostID] = nil
    }
}
