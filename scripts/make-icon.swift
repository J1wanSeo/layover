// Renders packaging/AppIcon.icns: white "airplane.departure" on a sky gradient.
// Usage: swift scripts/make-icon.swift   (from the repo root)
import AppKit

let out = URL(fileURLWithPath: "packaging/AppIcon.iconset", isDirectory: true)
try? FileManager.default.removeItem(at: out)
try FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)

func render(_ px: Int) -> Data {
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px,
                               bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                               colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)

    let s = CGFloat(px)
    // macOS icon grid: ~10% margin, continuous-ish corner radius.
    let inset = s * 0.1
    let rect = NSRect(x: inset, y: inset, width: s - 2 * inset, height: s - 2 * inset)
    let shape = NSBezierPath(roundedRect: rect, xRadius: rect.width * 0.225, yRadius: rect.width * 0.225)
    NSGradient(colors: [
        NSColor(srgbRed: 0.33, green: 0.72, blue: 1.00, alpha: 1),
        NSColor(srgbRed: 0.10, green: 0.36, blue: 0.85, alpha: 1),
    ])!.draw(in: shape, angle: -90)

    let config = NSImage.SymbolConfiguration(pointSize: rect.width * 0.5, weight: .semibold)
        .applying(.init(paletteColors: [.white]))
    if let plane = NSImage(systemSymbolName: "airplane.departure", accessibilityDescription: nil)?
        .withSymbolConfiguration(config) {
        let size = plane.size
        plane.draw(in: NSRect(x: rect.midX - size.width / 2, y: rect.midY - size.height / 2,
                              width: size.width, height: size.height))
    }

    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for base in [16, 32, 128, 256, 512] {
    try render(base).write(to: out.appendingPathComponent("icon_\(base)x\(base).png"))
    try render(base * 2).write(to: out.appendingPathComponent("icon_\(base)x\(base)@2x.png"))
}

let p = Process()
p.executableURL = URL(fileURLWithPath: "/usr/bin/iconutil")
p.arguments = ["-c", "icns", out.path, "-o", "packaging/AppIcon.icns"]
try p.run()
p.waitUntilExit()
try FileManager.default.removeItem(at: out)
print(p.terminationStatus == 0 ? "wrote packaging/AppIcon.icns" : "iconutil failed")
