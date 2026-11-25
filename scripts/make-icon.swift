// Renders app/AppIcon.icns: the tile is a kraft paper folder (dark back, light
// front with a tab top left) with a shipping label (QR code, address lines,
// red stamp box). macOS 26 puts icons that do not fill the tile shape into a
// grey plate, so the folder fills the whole tile instead of cutting out a corner.
// Run: swift scripts/make-icon.swift [preview.png] [folder-only.png]
// The folder-only image is a free-standing folder with the same label,
// cropped with room for the shadow (for web pages).
import AppKit

let sizes = [16, 32, 64, 128, 256, 512, 1024]
let fm = FileManager.default
let iconset = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("AppIcon.iconset")
try? fm.removeItem(at: iconset)
try fm.createDirectory(at: iconset, withIntermediateDirectories: true)

func rgb(_ hex: UInt32, _ a: CGFloat = 1) -> CGColor {
    CGColor(srgbRed: CGFloat(hex >> 16 & 0xff) / 255, green: CGFloat(hex >> 8 & 0xff) / 255,
            blue: CGFloat(hex & 0xff) / 255, alpha: a)
}

let ink = rgb(0x1f1b17)
let paper = rgb(0xfbf8f1)
let stamp = rgb(0xd8432a)

func gradient(_ c: CGContext, _ a: CGColor, _ b: CGColor, _ y0: CGFloat, _ y1: CGFloat) {
    let g = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB), colors: [a, b] as CFArray, locations: [0, 1])!
    c.drawLinearGradient(g, start: CGPoint(x: 0, y: y0), end: CGPoint(x: 0, y: y1), options: [.drawsBeforeStartLocation, .drawsAfterEndLocation])
}

func rounded(_ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat, _ r: CGFloat) -> CGPath {
    CGPath(roundedRect: CGRect(x: x, y: y, width: w, height: h), cornerWidth: r, cornerHeight: r, transform: nil)
}

/// Small seeded random (mulberry32), so the QR pattern is the same every time.
struct Rand {
    var a: UInt32
    mutating func next() -> Double {
        a &+= 0x6D2B79F5
        var t = (a ^ (a >> 15)) &* (1 | a)
        t = (t &+ ((t ^ (t >> 7)) &* (61 | t))) ^ t
        return Double(t ^ (t >> 14)) / 4294967296
    }
}

/// A QR-like pattern: three finder squares, random modules elsewhere.
func qr(_ c: CGContext, _ x: CGFloat, _ y: CGFloat, _ size: CGFloat, _ n: Int, seed: UInt32) {
    let m = size / CGFloat(n)
    var r = Rand(a: seed)
    let inFinder = { (i: Int, j: Int) in (i < 8 && j < 8) || (i >= n - 8 && j < 8) || (i < 8 && j >= n - 8) }
    for (cx, cy) in [(0, 0), (n - 7, 0), (0, n - 7)] {
        let X = x + CGFloat(cx) * m, Y = y + CGFloat(cy) * m
        c.setFillColor(ink); c.fill(CGRect(x: X, y: Y, width: 7 * m, height: 7 * m))
        c.setFillColor(paper); c.fill(CGRect(x: X + m, y: Y + m, width: 5 * m, height: 5 * m))
        c.setFillColor(ink); c.fill(CGRect(x: X + 2 * m, y: Y + 2 * m, width: 3 * m, height: 3 * m))
    }
    c.setFillColor(ink)
    for j in 0..<n {
        for i in 0..<n where r.next() < 0.5 && !inFinder(i, j) {
            c.fill(CGRect(x: x + CGFloat(i) * m, y: y + CGFloat(j) * m, width: m * 1.02, height: m * 1.02))
        }
    }
}

/// The shipping label, straight, centered at (cx, cy). Its height is 5/8 of `w`.
/// The QR code has few modules, so its finder squares stay visible in small sizes.
func label(_ c: CGContext, scale: CGFloat, w: CGFloat, cx: CGFloat, cy: CGFloat) {
    let k = w / 400, h = 250 * k, x = cx - w / 2, y = cy - h / 2
    c.saveGState()
    c.setShadow(offset: CGSize(width: 0, height: -10 * scale), blur: 24 * scale, color: rgb(0x4a3418, 0.32))
    c.setFillColor(paper); c.addPath(rounded(x, y, w, h, 14 * k)); c.fillPath()
    c.restoreGState()
    qr(c, x + 24 * k, y + 24 * k, 202 * k, 17, seed: 7)
    c.setFillColor(ink); c.addPath(rounded(x + 250 * k, y + 30 * k, 124 * k, 22 * k, 5 * k)); c.fillPath()
    c.setFillColor(rgb(0x1f1b17, 0.45))
    c.addPath(rounded(x + 250 * k, y + 72 * k, 110 * k, 13 * k, 4 * k)); c.fillPath()
    c.addPath(rounded(x + 250 * k, y + 98 * k, 90 * k, 13 * k, 4 * k)); c.fillPath()
    c.setStrokeColor(stamp); c.setLineWidth(9 * k); c.setLineCap(.round)
    c.addPath(rounded(x + 250 * k, y + 150 * k, 124 * k, 74 * k, 8 * k)); c.strokePath()
    c.move(to: CGPoint(x: x + 272 * k, y: y + 188 * k)); c.addLine(to: CGPoint(x: x + 352 * k, y: y + 188 * k)); c.strokePath()
    c.restoreGState()
}

// Draws on a 1024 grid with y pointing down. `scale` converts to device pixels
// for the shadow, which CoreGraphics does not scale with the CTM.
func draw(_ c: CGContext, scale: CGFloat, tile: Bool = true) {
    if tile { drawTile(c, scale: scale) } else { drawFolder(c, scale: scale) }
}

/// App icon: the tile is the folder back, the front panel has the tab.
func drawTile(_ c: CGContext, scale: CGFloat) {
    let tilePath = rounded(100, 100, 824, 824, 185)
    c.saveGState()
    c.setShadow(offset: CGSize(width: 0, height: -6 * scale), blur: 16 * scale, color: rgb(0x000000, 0.2))
    c.setFillColor(rgb(0xa98252)); c.addPath(tilePath); c.fillPath()
    c.restoreGState()

    c.saveGState()
    c.addPath(tilePath); c.clip()
    gradient(c, rgb(0xa98252), rgb(0x8f683b), 100, 924)

    // Front panel with the tab top left. It runs past the tile edges; the clip cuts it.
    let top: CGFloat = 330, tabTop: CGFloat = 210
    let front = CGMutablePath()
    front.move(to: CGPoint(x: 0, y: tabTop))
    front.addLine(to: CGPoint(x: 400, y: tabTop))
    front.addCurve(to: CGPoint(x: 510, y: top), control1: CGPoint(x: 455, y: tabTop), control2: CGPoint(x: 455, y: top))
    front.addLine(to: CGPoint(x: 1024, y: top))
    front.addLine(to: CGPoint(x: 1024, y: 1024))
    front.addLine(to: CGPoint(x: 0, y: 1024))
    front.closeSubpath()
    c.saveGState()
    c.setShadow(offset: CGSize(width: 0, height: 8 * scale), blur: 20 * scale, color: rgb(0x3a2810, 0.4))
    c.setFillColor(rgb(0xd8b685)); c.addPath(front); c.fillPath()
    c.restoreGState()
    c.saveGState(); c.addPath(front); c.clip(); gradient(c, rgb(0xe4c697), rgb(0xc49a66), tabTop, 924); c.restoreGState()
    c.addPath(front); c.setStrokeColor(rgb(0xffffff, 0.3)); c.setLineWidth(6); c.strokePath()

    label(c, scale: scale, w: 600, cx: 512, cy: 625)
    c.restoreGState()
}

/// Free-standing folder for the web pages.
func drawFolder(_ c: CGContext, scale: CGFloat) {
    func shadow() {
        c.setShadow(offset: CGSize(width: 0, height: -12 * scale), blur: 30 * scale, color: rgb(0x4a3418, 0.28))
    }

    // Folder back with tab.
    let back = CGMutablePath()
    back.move(to: CGPoint(x: 200, y: 330))
    back.addQuadCurve(to: CGPoint(x: 250, y: 280), control: CGPoint(x: 200, y: 280))
    back.addLine(to: CGPoint(x: 420, y: 280))
    back.addQuadCurve(to: CGPoint(x: 455, y: 295), control: CGPoint(x: 440, y: 280))
    back.addLine(to: CGPoint(x: 490, y: 330))
    back.addLine(to: CGPoint(x: 774, y: 330))
    back.addQuadCurve(to: CGPoint(x: 824, y: 380), control: CGPoint(x: 824, y: 330))
    back.addLine(to: CGPoint(x: 824, y: 720))
    back.addQuadCurve(to: CGPoint(x: 774, y: 770), control: CGPoint(x: 824, y: 770))
    back.addLine(to: CGPoint(x: 250, y: 770))
    back.addQuadCurve(to: CGPoint(x: 200, y: 720), control: CGPoint(x: 200, y: 770))
    back.closeSubpath()

    // Folder front.
    let front = CGMutablePath()
    front.move(to: CGPoint(x: 200, y: 420))
    front.addQuadCurve(to: CGPoint(x: 240, y: 380), control: CGPoint(x: 200, y: 380))
    front.addLine(to: CGPoint(x: 784, y: 380))
    front.addQuadCurve(to: CGPoint(x: 824, y: 420), control: CGPoint(x: 824, y: 380))
    front.addLine(to: CGPoint(x: 824, y: 720))
    front.addQuadCurve(to: CGPoint(x: 774, y: 770), control: CGPoint(x: 824, y: 770))
    front.addLine(to: CGPoint(x: 250, y: 770))
    front.addQuadCurve(to: CGPoint(x: 200, y: 720), control: CGPoint(x: 200, y: 770))
    front.closeSubpath()

    c.saveGState()
    shadow()
    c.beginTransparencyLayer(auxiliaryInfo: nil)
    c.saveGState(); c.addPath(back); c.clip(); gradient(c, rgb(0xb58c5a), rgb(0x9c7444), 280, 770); c.restoreGState()
    c.saveGState(); c.addPath(front); c.clip(); gradient(c, rgb(0xdcbb8c), rgb(0xc49a66), 380, 770); c.restoreGState()
    c.endTransparencyLayer()
    c.restoreGState()

    // Highlight on the front edge.
    let edge = CGMutablePath()
    edge.move(to: CGPoint(x: 200, y: 420))
    edge.addQuadCurve(to: CGPoint(x: 240, y: 380), control: CGPoint(x: 200, y: 380))
    edge.addLine(to: CGPoint(x: 784, y: 380))
    edge.addQuadCurve(to: CGPoint(x: 824, y: 420), control: CGPoint(x: 824, y: 380))
    c.addPath(edge); c.setStrokeColor(rgb(0xffffff, 0.25)); c.setLineWidth(6); c.strokePath()

    label(c, scale: scale, w: 400, cx: 512, cy: 575)
}

func render(_ px: Int, tile: Bool = true) -> Data {
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                               samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                               bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    let ctx = NSGraphicsContext(bitmapImageRep: rep)!
    NSGraphicsContext.current = ctx
    let c = ctx.cgContext
    let s = CGFloat(px) / 1024
    c.translateBy(x: 0, y: CGFloat(px))
    c.scaleBy(x: s, y: -s)
    draw(c, scale: s, tile: tile)
    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for px in sizes {
    if px <= 512 { try render(px).write(to: iconset.appendingPathComponent("icon_\(px)x\(px).png")) }
    if px >= 32 { try render(px).write(to: iconset.appendingPathComponent("icon_\(px / 2)x\(px / 2)@2x.png")) }
}
if CommandLine.arguments.count > 1 {
    try render(1024).write(to: URL(fileURLWithPath: CommandLine.arguments[1]))
}
if CommandLine.arguments.count > 2 {
    // Folder without tile, cropped with room for the shadow.
    let full = NSBitmapImageRep(data: render(1024, tile: false))!.cgImage!
    let crop = full.cropping(to: CGRect(x: 160, y: 236, width: 704, height: 600))!
    let rep = NSBitmapImageRep(cgImage: crop)
    try rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: CommandLine.arguments[2]))
}
let p = Process()
p.executableURL = URL(fileURLWithPath: "/usr/bin/iconutil")
p.arguments = ["-c", "icns", iconset.path, "-o", "app/AppIcon.icns"]
try p.run()
p.waitUntilExit()
print("app/AppIcon.icns")
