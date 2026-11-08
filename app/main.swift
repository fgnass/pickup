// Pickup: a Finder service that shows a shipping label with a QR code for
// files and folders.
// The phone scans it and downloads the files directly over the local network.
//
// The app starts when the user picks "Share via QR Code" in the Finder context
// menu. It runs the bundled pickup-helper for each selection and quits when
// the last QR window closes.

import AppKit
import CoreImage.CIFilterBuiltins
import SwiftUI

// MARK: - Session (one helper process + one window)

@MainActor
@Observable
final class Session {
    enum Phase: Equatable {
        case starting
        case waiting
        case connected(Int)
        case sending(name: String, sent: Int64, total: Int64)
        case sent(name: String)
        case failed(String)
        case ended(delivered: Bool)
    }

    var title: String
    var subtitle = ""
    /// Name shown as sender. `defaults write buzz.gnass.pickup MacName "…"` overrides it.
    let from = UserDefaults.standard.string(forKey: "MacName") ?? Host.current().localizedName ?? "Mac"
    var url: String?
    var phase: Phase = .starting

    private let process = Process()
    private let stdin = Pipe()
    private var buffer = Data()
    private var peers = 0
    var onEnd: (() -> Void)?

    init(urls: [URL]) {
        title = urls.count == 1 ? urls[0].lastPathComponent : "\(urls.count) items"
        guard let helper = Bundle.main.url(forAuxiliaryExecutable: "pickup-helper") else {
            phase = .failed("The helper is missing from the app bundle.")
            return
        }
        process.executableURL = helper
        process.arguments = ["-name", from, "--"] + urls.map(\.path)
        process.standardInput = stdin
        let out = Pipe()
        process.standardOutput = out
        process.standardError = FileHandle.nullDevice
        out.fileHandleForReading.readabilityHandler = { [weak self] h in
            let data = h.availableData
            Task { @MainActor in self?.receive(data) }
        }
        process.terminationHandler = { [weak self] p in
            Task { @MainActor in self?.terminated(status: p.terminationStatus) }
        }
        do {
            try process.run()
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    func stop() {
        if process.isRunning { process.terminate() }
    }

    private func receive(_ data: Data) {
        buffer.append(data)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer[buffer.startIndex..<nl]
            buffer.removeSubrange(buffer.startIndex...nl)
            if let obj = try? JSONSerialization.jsonObject(with: line) as? [String: Any] {
                handle(obj)
            }
        }
    }

    private func handle(_ e: [String: Any]) {
        let num = { (k: String) in (e[k] as? NSNumber)?.int64Value ?? 0 }
        switch e["type"] as? String {
        case "ready":
            url = e["url"] as? String
            title = e["title"] as? String ?? title
            subtitle = e["subtitle"] as? String ?? ""
            phase = .waiting
        case "peers":
            peers = Int(num("count"))
            if case .sending = phase { return }
            phase = peers > 0 ? .connected(peers) : .waiting
        case "progress":
            phase = .sending(name: e["name"] as? String ?? "", sent: num("sent"), total: num("total"))
        case "sent":
            phase = .sent(name: e["name"] as? String ?? "")
        case "ended":
            phase = .ended(delivered: e["reason"] as? String == "delivered")
            // Let the user see the stamp, then close the window.
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(3))
                self.onEnd?()
            }
        case "error":
            phase = .failed(e["message"] as? String ?? "Unknown error")
        default:
            break
        }
    }

    private func terminated(status: Int32) {
        if status != 0, case .failed = phase {} else if status != 0 {
            phase = .failed("The helper stopped unexpectedly.")
        }
    }
}

// MARK: - Views

/// Colours of the shipping label look.
enum Kraft {
    static let kraft = Color(red: 0.788, green: 0.635, blue: 0.455)  // #c9a274
    static let onKraft = Color(red: 0.420, green: 0.345, blue: 0.247) // #6b583f
    static let paper = Color(red: 0.984, green: 0.973, blue: 0.945)  // #fbf8f1
    static let ink = Color(red: 0.122, green: 0.106, blue: 0.090)    // #1f1b17
    static let muted = Color(red: 0.467, green: 0.427, blue: 0.376)  // #776d60
    static let stamp = Color(red: 0.847, green: 0.263, blue: 0.165)  // #d8432a
}

struct QRCode: View {
    let text: String

    var body: some View {
        if let image = Self.render(text) {
            Image(nsImage: image)
                .interpolation(.none)
                .resizable()
                .aspectRatio(1, contentMode: .fit)
        }
    }

    /// Renders the code in label ink on transparent background.
    static func render(_ text: String) -> NSImage? {
        let f = CIFilter.qrCodeGenerator()
        f.message = Data(text.utf8)
        f.correctionLevel = "L" // shown on a screen: no damage to correct, fewer modules
        guard let code = f.outputImage else { return nil }
        let tint = CIFilter.falseColor()
        tint.inputImage = code
        tint.color0 = CIColor(red: 0.122, green: 0.106, blue: 0.090)
        tint.color1 = CIColor(red: 0, green: 0, blue: 0, alpha: 0) // transparent: shows the label
        guard let ci = tint.outputImage,
              let cg = CIContext().createCGImage(ci, from: code.extent)
        else { return nil }
        return NSImage(cgImage: cg, size: NSSize(width: code.extent.width, height: code.extent.height))
    }
}

/// Decorative barcode, derived from the text so it stays the same per share.
struct Barcode: View {
    let text: String

    var body: some View {
        Canvas { ctx, size in
            var h: UInt32 = 2166136261
            for b in text.utf8 { h = (h ^ UInt32(b)) &* 16777619 }
            var x: CGFloat = 0
            var bar = true
            while x < size.width {
                h = h &* 1103515245 &+ 12345
                let w = CGFloat(1 + (h >> 16) % 3)
                if bar { ctx.fill(Path(CGRect(x: x, y: 0, width: w, height: size.height)), with: .color(Kraft.ink)) }
                x += w
                bar.toggle()
            }
        }
    }
}

/// Kraft paper: base colour, fine fibres and a soft light spot (as on the phone).
struct KraftPaper: View {
    var body: some View {
        ZStack {
            Kraft.kraft
            Canvas { ctx, size in
                ctx.rotate(by: .degrees(7))
                var x: CGFloat = -size.height
                while x < size.width + size.height {
                    ctx.fill(Path(CGRect(x: x, y: -size.height, width: 2, height: size.height * 3)),
                             with: .color(.black.opacity(0.025)))
                    x += 5
                }
            }
            RadialGradient(colors: [.white.opacity(0.14), .clear],
                           center: UnitPoint(x: 0.2, y: 0), startRadius: 0, endRadius: 260)
        }
    }
}

struct Key: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text.uppercased())
            .font(.system(size: 9, weight: .heavy))
            .tracking(1.3)
            .foregroundStyle(Kraft.muted)
    }
}

struct SessionView: View {
    let session: Session

    var body: some View {
        // Only the label sits on the kraft paper; the status is part of it.
        label
        .padding(.horizontal, 16)
        .padding(.top, 36)
        .padding(.bottom, 16)
        .frame(width: 340)
        .background(KraftPaper().ignoresSafeArea())
        .environment(\.colorScheme, .light)
    }

    var label: some View {
        VStack(spacing: 0) {
            labelContent.padding(16)
            status
                .font(.system(size: 10.5, weight: .heavy))
                .tracking(1.3)
                .textCase(.uppercase)
                .foregroundStyle(.white)
                .tint(.white)
                .frame(maxWidth: .infinity, minHeight: 36)
                .padding(.horizontal, 12)
                .background(isFailed ? Kraft.stamp : Kraft.ink)
        }
        .background(.white)
        .clipShape(RoundedRectangle(cornerRadius: 10))
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(.white)
                .shadow(color: Color(red: 0.24, green: 0.16, blue: 0.06).opacity(0.12), radius: 1.5, y: 1)
                .shadow(color: Color(red: 0.24, green: 0.16, blue: 0.06).opacity(0.24), radius: 16, y: 8)
        )
    }

    var isFailed: Bool {
        if case .failed = session.phase { return true }
        return false
    }

    var labelContent: some View {
        VStack(spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                Text("PICKUP")
                    .font(.system(size: 22, weight: .black).width(.compressed))
                    .tracking(0.6)
                Spacer()
                Text("LOCAL · ENCRYPTED")
                    .font(.system(size: 9.5, weight: .heavy))
                    .tracking(1)
                    .foregroundStyle(Kraft.paper)
                    .padding(.horizontal, 7)
                    .padding(.vertical, 3)
                    .background(Kraft.ink)
            }
            .padding(.bottom, 8)
            Rectangle().fill(Kraft.ink).frame(height: 2)

            HStack(alignment: .top, spacing: 0) {
                field("From", session.from)
                Rectangle().fill(Kraft.ink).frame(width: 1)
                field("To", "The phone that scans this").padding(.leading, 10)
            }
            .fixedSize(horizontal: false, vertical: true)
            Rectangle().fill(Kraft.ink).frame(height: 1)

            code
                .frame(width: 210, height: 210)
                .padding(.vertical, 14)

            Rectangle().fill(Kraft.ink).frame(height: 1)
            HStack(alignment: .bottom, spacing: 10) {
                VStack(alignment: .leading, spacing: 1) {
                    Key("Contents")
                    Text(session.title)
                        .font(.system(size: 15, weight: .semibold))
                        .lineLimit(1)
                        .truncationMode(.middle)
                    if !session.subtitle.isEmpty {
                        Text(session.subtitle)
                            .font(.system(size: 11.5, weight: .medium))
                            .foregroundStyle(Kraft.muted)
                    }
                }
                Spacer(minLength: 0)
                Barcode(text: session.url ?? session.title).frame(width: 96, height: 30)
            }
            .padding(.top, 8)
        }
        .foregroundStyle(Kraft.ink)
    }

    func field(_ key: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Key(key)
            Text(value)
                .font(.system(size: 13.5, weight: .semibold))
                .lineLimit(2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 8)
    }

    @ViewBuilder var code: some View {
        ZStack {
            if let url = session.url {
                QRCode(text: url)
                    .opacity(ended == nil ? 1 : 0.18)
            } else if case .failed = session.phase {
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 44))
                    .foregroundStyle(Kraft.stamp)
            } else {
                ProgressView()
            }
            if let delivered = ended {
                Text(delivered ? "Delivered" : "Returned")
                    .font(.system(size: 34, weight: .black).width(.condensed))
                    .tracking(1.5)
                    .textCase(.uppercase)
                    .foregroundStyle(Kraft.stamp)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 4)
                    .overlay(RoundedRectangle(cornerRadius: 7).stroke(Kraft.stamp, lineWidth: 3.5))
                    .rotationEffect(.degrees(-12))
                    .transition(.scale(scale: 1.6).combined(with: .opacity))
            }
        }
        .animation(.spring(duration: 0.35, bounce: 0.3), value: ended)
    }

    var ended: Bool? {
        if case .ended(let delivered) = session.phase { return delivered }
        return nil
    }

    @ViewBuilder var status: some View {
        switch session.phase {
        case .starting:
            Text("Starting…")
        case .waiting:
            Label("Scan with your phone camera", systemImage: "camera.viewfinder")
        case .connected(let n):
            Label(n > 1 ? "\(n) phones connected" : "Phone connected", systemImage: "checkmark.circle.fill")
        case let .sending(name, sent, total):
            VStack(spacing: 5) {
                ProgressView(value: Double(sent), total: Double(max(total, sent, 1)))
                Text("Sending \(name)")
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        case .sent(let name):
            Label("Sent \(name)", systemImage: "checkmark.circle.fill")
                .lineLimit(1)
                .truncationMode(.middle)
        case .ended(let delivered):
            Text(delivered ? "Picked up · Sharing stopped" : "Not picked up · Sharing stopped")
        case .failed(let msg):
            Text(msg)
                .textCase(nil)
                .tracking(0)
                .font(.system(size: 12))
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.vertical, 8)
        }
    }
}

// MARK: - Window

final class SessionWindow: NSPanel {
    override var canBecomeKey: Bool { true }

    // Esc closes the window.
    override func cancelOperation(_ sender: Any?) { close() }
}

// MARK: - App

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private var sessions: [ObjectIdentifier: (Session, NSWindow)] = [:]
    private var helpWindow: NSWindow?

    func applicationDidFinishLaunching(_ note: Notification) {
        NSApp.servicesProvider = self
        NSUpdateDynamicServices()
        // Started without files (for example from the Applications folder):
        // explain how to use the app.
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
            if self.sessions.isEmpty { self.showHelp() }
        }
    }

    func application(_ app: NSApplication, open urls: [URL]) {
        start(urls.filter(\.isFileURL))
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool { true }

    func applicationWillTerminate(_ note: Notification) {
        for (s, _) in sessions.values { s.stop() }
    }

    /// Finder service entry point (NSMessage "shareViaQRCode" in Info.plist).
    @objc func shareViaQRCode(_ pboard: NSPasteboard, userData: String?, error: AutoreleasingUnsafeMutablePointer<NSString?>) {
        let urls = pboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL] ?? []
        if urls.isEmpty {
            error.pointee = "No files selected." as NSString
            return
        }
        start(urls)
    }

    private func start(_ urls: [URL]) {
        guard !urls.isEmpty else { return }
        helpWindow?.close()
        let session = Session(urls: urls)
        let window = SessionWindow(
            contentRect: .zero,
            styleMask: [.titled, .closable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.titlebarAppearsTransparent = true
        window.appearance = NSAppearance(named: .aqua)
        window.backgroundColor = NSColor(red: 0.780, green: 0.639, blue: 0.482, alpha: 1)
        window.titleVisibility = .hidden
        window.isMovableByWindowBackground = true
        window.level = .floating
        window.hidesOnDeactivate = false
        window.isReleasedWhenClosed = false
        window.collectionBehavior = [.moveToActiveSpace, .fullScreenAuxiliary]
        window.contentView = NSHostingView(rootView: SessionView(session: session))
        window.setContentSize(window.contentView!.fittingSize)
        window.delegate = self
        placeWindow(window)
        session.onEnd = { [weak window] in window?.close() }
        sessions[ObjectIdentifier(window)] = (session, window)
        NSApp.activate()
        window.makeKeyAndOrderFront(nil)
    }

    /// Center the first window; cascade further ones.
    private func placeWindow(_ window: NSWindow) {
        if let last = sessions.values.map(\.1).last(where: \.isVisible) {
            let p = last.frame.origin
            window.setFrameOrigin(NSPoint(x: p.x + 28, y: p.y - 28))
        } else {
            window.center()
        }
    }

    func windowWillClose(_ note: Notification) {
        guard let window = note.object as? NSWindow else { return }
        if let (session, _) = sessions.removeValue(forKey: ObjectIdentifier(window)) {
            session.stop()
        }
    }

    private func showHelp() {
        let alert = NSAlert()
        alert.messageText = "Pickup"
        alert.informativeText = "Right-click a file or folder in the Finder and choose Share via QR Code. You can also drop files on the Pickup icon."
        alert.addButton(withTitle: "OK")
        NSApp.activate()
        alert.runModal()
        if sessions.isEmpty { NSApp.terminate(nil) }
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
