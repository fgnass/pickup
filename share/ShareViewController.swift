// Pickup share extension: puts Pickup into the Share menu of every app.
//
// The extension runs in the sandbox and shows no UI. It copies the shared
// items to a private pasteboard and opens "pickup://share" with the Pickup
// app that contains it. The app then reads the pasteboard and opens a label,
// like the Finder service does.

import AppKit
import ImageIO
import UniformTypeIdentifiers

/// Pasteboard and types that the app reads too (see `SharePasteboard` in app/main.swift).
private let pasteboardName = NSPasteboard.Name("buzz.gnass.pickup.share")
private let nameType = NSPasteboard.PasteboardType("buzz.gnass.pickup.name")
private let dataType = NSPasteboard.PasteboardType("buzz.gnass.pickup.data")

@objc(ShareViewController)
final class ShareViewController: NSViewController {
    override func loadView() {
        view = NSView(frame: .zero)
    }

    override func viewDidLoad() {
        super.viewDidLoad()
        let providers = (extensionContext?.inputItems as? [NSExtensionItem] ?? [])
            .flatMap { $0.attachments ?? [] }
        Task { @MainActor in
            var items: [NSPasteboardItem] = []
            for provider in providers {
                if let item = await Self.pasteboardItem(for: provider) { items.append(item) }
            }
            // Some apps send the selected text as the item's text, not as an attachment.
            if items.isEmpty,
               let text = (extensionContext?.inputItems.first as? NSExtensionItem)?.attributedContentText?.string,
               !text.isEmpty {
                let item = NSPasteboardItem()
                item.setString(text, forType: .string)
                items.append(item)
            }
            guard !items.isEmpty else {
                extensionContext?.cancelRequest(withError: NSError(domain: NSCocoaErrorDomain, code: NSFeatureUnsupportedError))
                return
            }
            let pboard = NSPasteboard(name: pasteboardName)
            pboard.clearContents()
            pboard.writeObjects(items)
            openApp()
            extensionContext?.completeRequest(returningItems: nil)
        }
    }

    /// The app that contains this extension: Pickup.app/Contents/PlugIns/PickupShare.appex.
    private func openApp() {
        let app = Bundle.main.bundleURL
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        let config = NSWorkspace.OpenConfiguration()
        config.activates = true
        NSWorkspace.shared.open([URL(string: "pickup://share")!], withApplicationAt: app, configuration: config)
    }

    /// File first, then link, then text, then raw data.
    private static func pasteboardItem(for provider: NSItemProvider) async -> NSPasteboardItem? {
        let item = NSPasteboardItem()
        if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier),
           let url = await loadURL(provider), url.isFileURL {
            item.setString(url.absoluteString, forType: .fileURL)
            return item
        }
        if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier),
           let url = await loadURL(provider) {
            if url.isFileURL {
                item.setString(url.absoluteString, forType: .fileURL)
            } else {
                item.setString(url.absoluteString, forType: .string)
            }
            return item
        }
        if provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier),
           let text = await loadText(provider) {
            item.setString(text, forType: .string)
            return item
        }
        // Content without a file, for example an image from a web page:
        // copy the bytes. The app saves them as a file.
        // Prefer a type with a file extension (public.png, not a private type).
        let types = provider.registeredTypeIdentifiers.compactMap(UTType.init).filter { $0.conforms(to: .data) }
        guard let type = types.first(where: { $0.preferredFilenameExtension != nil }) ?? types.first,
              var data = await loadData(provider, type: type)
        else { return nil }
        // An app that shares an NSImage sends an archived object, not image
        // bytes: save it as PNG.
        if type.conforms(to: .image), imageExtension(of: data) == nil,
           let image = try? NSKeyedUnarchiver.unarchivedObject(ofClass: NSImage.self, from: data),
           let tiff = image.tiffRepresentation,
           let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) {
            data = png
        }
        var name = provider.suggestedName ?? "Shared"
        // A general type such as public.image has no extension: look at the bytes.
        if (name as NSString).pathExtension.isEmpty,
           let ext = type.preferredFilenameExtension ?? imageExtension(of: data) {
            name += "." + ext
        }
        item.setString(name, forType: nameType)
        item.setData(data, forType: dataType)
        return item
    }

    private static func imageExtension(of data: Data) -> String? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let id = CGImageSourceGetType(source) as String?
        else { return nil }
        return UTType(id)?.preferredFilenameExtension
    }

    private static func loadURL(_ provider: NSItemProvider) async -> URL? {
        await withCheckedContinuation { cont in
            _ = provider.loadObject(ofClass: NSURL.self) { url, _ in
                cont.resume(returning: url as? URL)
            }
        }
    }

    private static func loadText(_ provider: NSItemProvider) async -> String? {
        await withCheckedContinuation { cont in
            _ = provider.loadObject(ofClass: NSString.self) { text, _ in
                cont.resume(returning: text as? String)
            }
        }
    }

    private static func loadData(_ provider: NSItemProvider, type: UTType) async -> Data? {
        await withCheckedContinuation { cont in
            _ = provider.loadDataRepresentation(for: type) { data, _ in
                cont.resume(returning: data)
            }
        }
    }
}
