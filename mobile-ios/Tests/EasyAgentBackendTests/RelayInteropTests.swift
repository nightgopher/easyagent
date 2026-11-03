import Foundation
import Testing
@testable import EasyAgentBackend

@Suite struct RelayInteropTests {
    @Test func nativeTLSAndWebSocketsThroughGoRelay() async throws { try await RelayInteropScenario.run() }

    @Test func rejectsInvalidPairingConfiguration() throws {
        let invalid = RelayConfig(relayURL: URL(string: "http://relay.example")!, routeID: String(repeating: "a", count: 43), fingerprint: "sha256:" + Data(repeating: 0, count: 32).base64EncodedString())
        #expect(throws: RelayError.self) { try invalid.validate() }
        #expect(throws: RelayError.self) { try RelayPairCode.parse("{}") }
        let valid = RelayConfig(relayURL: URL(string: "https://relay.example")!, routeID: String(repeating: "a", count: 43), fingerprint: invalid.fingerprint)
        try valid.validate()
        #expect(try valid.serverName() == "ea-00000000000000000000000000000000.invalid")
    }
}
