// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "Layover",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "Layover", path: "Sources/Layover"),
    ]
)
