// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "EasyAgent",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "EasyAgentBackend", targets: ["EasyAgentBackend"]),
    ],
    targets: [
        .target(name: "EasyAgentBackend"),
        .testTarget(name: "EasyAgentBackendTests", dependencies: ["EasyAgentBackend"]),
    ]
)
