import Foundation

// MARK: - Wire types
// 字段与 mobile-native/backend/Wire.kt 的 SessionSummary 等契约一一对应。

public struct SessionSummary: Codable, Equatable, Identifiable, Sendable {
    public var id: String
    public var title: String
    public var lastActive: Double
    public var createdAt: Double
    public var workspace: String
    public var application: String
    public var archived: Bool
    public var pinned: Bool
    /// "running" or "waiting_confirmation" while a run is active, else empty.
    public var runState: String
    public var pendingConfirmations: Int

    public init(id: String = "", title: String = "", lastActive: Double = 0, createdAt: Double = 0,
                workspace: String = "", application: String = "", archived: Bool = false,
                pinned: Bool = false, runState: String = "", pendingConfirmations: Int = 0) {
        self.id = id
        self.title = title
        self.lastActive = lastActive
        self.createdAt = createdAt
        self.workspace = workspace
        self.application = application
        self.archived = archived
        self.pinned = pinned
        self.runState = runState
        self.pendingConfirmations = pendingConfirmations
    }

    enum CodingKeys: String, CodingKey {
        case id, title, workspace, application, archived, pinned
        case lastActive = "last_active"
        case createdAt = "created_at"
        case runState = "run_state"
        case pendingConfirmations = "pending_confirmations"
    }
}

public struct HealthResponse: Codable, Sendable {
    public var status: String
    public var version: String?
}

public enum APIError: Error, Equatable, Sendable {
    case unauthorized
    case unreachable(String)
    case invalidResponse(Int)
    case decode(String)
}

// MARK: - Host

public struct Host: Codable, Equatable, Identifiable, Sendable {
    public var id: UUID
    public var name: String
    /// Base URL, e.g. http://100.64.0.1:8080 — no trailing slash.
    public var baseURL: URL
    public var createdAt: Date

    public init(id: UUID = UUID(), name: String, baseURL: URL, createdAt: Date = Date()) {
        self.id = id
        self.name = name
        self.baseURL = baseURL
        self.createdAt = createdAt
    }
}

public actor HostStore {
    /// Non-secret host list. The API key never lives here; APIClient takes it
    /// from the Keychain at call time (App side) so stores stay log-safe.
    private var hosts: [Host] = []
    private let defaults: UserDefaults
    private let storageKey = "easyagent.hosts"

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        if let data = defaults.data(forKey: storageKey),
           let decoded = try? JSONDecoder().decode([Host].self, from: data) {
            hosts = decoded
        }
    }

    public func list() -> [Host] { hosts }

    public func add(_ host: Host) {
        hosts.removeAll { $0.baseURL == host.baseURL }
        hosts.append(host)
        persist()
    }

    public func remove(id: UUID) {
        hosts.removeAll { $0.id == id }
        persist()
    }

    public func update(_ host: Host) {
        guard let index = hosts.firstIndex(where: { $0.id == host.id }) else { return }
        hosts[index] = host
        persist()
    }

    private func persist() {
        if let data = try? JSONEncoder().encode(hosts) {
            defaults.set(data, forKey: storageKey)
        }
    }
}

// MARK: - API client

public actor APIClient {
    private let host: Host
    private let apiKeyProvider: @Sendable () -> String
    private let session: URLSession
    private let now: @Sendable () -> Date

    public init(host: Host, apiKeyProvider: @escaping @Sendable () -> String,
                session: URLSession = .shared, now: @escaping @Sendable () -> Date = Date.init) {
        self.host = host
        self.apiKeyProvider = apiKeyProvider
        self.session = session
        self.now = now
    }

    public func health() async throws -> HealthResponse {
        try await get("health")
    }

    public func sessions() async throws -> [SessionSummary] {
        struct Wrapper: Codable { let sessions: [SessionSummary]? }
        let wrapper: Wrapper = try await get("sessions")
        return wrapper.sessions ?? []
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        var request = URLRequest(url: host.baseURL.appendingPathComponent(path))
        request.setValue("Bearer \(apiKeyProvider())", forHTTPHeaderField: "Authorization")
        do {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else {
                throw APIError.invalidResponse(-1)
            }
            switch http.statusCode {
            case 200:
                do { return try JSONDecoder().decode(T.self, from: data) }
                catch { throw APIError.decode(String(describing: T.self)) }
            case 401, 403: throw APIError.unauthorized
            default: throw APIError.invalidResponse(http.statusCode)
            }
        } catch let error as APIError {
            throw error
        } catch {
            throw APIError.unreachable(error.localizedDescription)
        }
    }
}
