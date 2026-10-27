// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "EasyAgent",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "EasyAgentBackend", targets: ["EasyAgentBackend"]),
        .library(name: "EasyAgentUI", targets: ["EasyAgentUI"]),
    ],
    targets: [
        .target(name: "EasyAgentBackend"),
        .target(name: "EasyAgentUI", dependencies: ["EasyAgentBackend"]),
        .testTarget(name: "EasyAgentBackendTests", dependencies: ["EasyAgentBackend"]),
        .testTarget(name: "EasyAgentUITests", dependencies: ["EasyAgentUI", "EasyAgentBackend"]),
    ]
)
