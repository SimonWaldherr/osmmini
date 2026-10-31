import OSMmini
import SwiftUI

struct DemoPlaceSearch: View {
  @Bindable var model: DemoModel
  let target: DemoModel.RoutePoint?
  let showRegions: () -> Void
  let selected: (DemoPlace) -> Void
  @Environment(\.dismiss) private var dismiss
  @Environment(\.dynamicTypeSize) private var dynamicTypeSize
  @FocusState private var focused: Bool
  @State private var presentationID = UUID()
  @State private var renaming: DemoPlaceLibrary.Entry?
  @State private var editedName = ""
  @State private var clearingRecent = false
  @State private var resettingLibrary = false

  private var trimmedQuery: String {
    model.query.trimmingCharacters(in: .whitespacesAndNewlines)
  }

  var body: some View {
    let personal = model.places.personalMatches(query: model.query)
    NavigationStack {
      VStack(spacing: 0) {
        VStack(alignment: .leading, spacing: 10) {
          HStack(spacing: 8) {
            Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
            TextField("Ort, Straße, Adresse oder POI", text: $model.query)
              .textFieldStyle(.plain)
              .autocorrectionDisabled().submitLabel(.search)
              .focused($focused).onSubmit { focused = false }
              .accessibilityLabel("Orte und eigene gespeicherte Orte suchen")
            if !model.query.isEmpty {
              Button {
                model.query = ""
                focused = true
              } label: {
                Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
                  .frame(minWidth: 44, minHeight: 44)
              }.buttonStyle(.plain).accessibilityLabel("Suchtext löschen")
            }
          }
          .padding(.horizontal, 12).frame(minHeight: 48)
          .background(.quaternary, in: RoundedRectangle(cornerRadius: 12))
          Label(
            model.engine?.manifest.name ?? "Keine Offline-Karte geöffnet",
            systemImage: model.engine == nil ? "map" : "checkmark.circle"
          ).font(.caption).foregroundStyle(.secondary)
        }.padding()
        searchContent(personal)
      }
      .navigationTitle(
        target == .start ? "Start suchen" : target == .destination ? "Ziel suchen" : "Orte suchen"
      )
      #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
      #endif
      .toolbar {
        ToolbarItem(placement: .cancellationAction) {
          Button("Schließen", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction)
        }
        if !model.places.recent.isEmpty {
          ToolbarItem(placement: .primaryAction) {
            Menu {
              Button("Letzte Auswahlen löschen", systemImage: "trash", role: .destructive) {
                clearingRecent = true
              }
            } label: {
              Label("Ortsliste verwalten", systemImage: "ellipsis.circle")
            }
          }
        }
      }
    }
    #if os(macOS)
      .frame(minWidth: 420, idealWidth: 560, minHeight: 450, idealHeight: 620)
    #else
      .presentationDetents([.large])
      .presentationDragIndicator(.visible)
    #endif
    .onAppear {
      model.resumeSearch(for: presentationID)
      #if os(macOS)
        focused = true
      #else
        focused = !trimmedQuery.isEmpty || (model.places.saved.isEmpty && model.places.recent.isEmpty)
      #endif
    }
    .onDisappear { model.suspendSearch(for: presentationID) }
    .alert(
      "Ort umbenennen",
      isPresented: Binding(
        get: { renaming != nil }, set: { if !$0 { renaming = nil } }
      )
    ) {
      TextField("Name", text: $editedName)
      Button("Speichern") {
        if let renaming { model.places.renameSaved(id: renaming.id, name: editedName) }
        renaming = nil
      }.disabled(editedName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
      Button("Abbrechen", role: .cancel) { renaming = nil }
    } message: {
      Text("Zum Beispiel Zuhause, Arbeit oder ein eigener Ortsname.")
    }
    .confirmationDialog(
      "Letzte Auswahlen löschen?", isPresented: $clearingRecent, titleVisibility: .visible
    ) {
      Button("Letzte Auswahlen löschen", role: .destructive) { model.places.clearRecent() }
      Button("Abbrechen", role: .cancel) {}
    } message: {
      Text("Gespeicherte Orte bleiben erhalten.")
    }
    .confirmationDialog(
      "Persönliche Orte zurücksetzen?", isPresented: $resettingLibrary, titleVisibility: .visible
    ) {
      Button("Persönliche Orte löschen", role: .destructive) { model.places.reset() }
      Button("Abbrechen", role: .cancel) {}
    } message: {
      Text(
        "Die nicht lesbaren gespeicherten Orte und letzten Auswahlen werden entfernt. Deine Offline-Karten bleiben erhalten."
      )
    }
  }

  private func searchContent(_ personal: DemoPlaceLibrary.Matches) -> some View {
    List {
      if let error = model.places.error {
        Section {
          Label(error, systemImage: "exclamationmark.triangle")
            .font(.callout).foregroundStyle(.red)
          if model.places.needsReset {
            Button("Persönliche Orte zurücksetzen …") { resettingLibrary = true }
          } else {
            Button("Hinweis schließen") { model.places.dismissError() }
          }
        }
      }
      personalPlaces(personal)
      if model.engine == nil {
        Section { searchStatus(personal) }
      } else if trimmedQuery.isEmpty {
        Section {
          LazyVGrid(
            columns: Array(
              repeating: GridItem(.flexible(), alignment: .leading),
              count: dynamicTypeSize.isAccessibilitySize ? 1 : 2), spacing: 8
          ) {
            ForEach(DemoSearchSuggestion.forPreset(model.engine?.manifest.poiPreset)) { suggestion in
              suggestedSearch(suggestion.query, image: suggestion.symbol)
            }
          }.buttonStyle(.bordered)
        } header: {
          Text("Kategorien in dieser Region")
        } footer: {
          Text(
            "Gespeicherte Orte und die letzten 20 Auswahlen bleiben lokal erhalten. Einen Ort auf der Karte öffnen und mit dem Stern speichern."
          )
          .font(.caption).fontWeight(.regular).foregroundStyle(.secondary)
          .lineLimit(nil).fixedSize(horizontal: false, vertical: true)
        }
      } else if !model.searching, model.searchError == nil, !model.results.isEmpty {
        searchResults
      } else {
        Section { searchStatus(personal) }
      }
    }.listStyle(.plain).scrollDismissesKeyboard(.interactively)
  }

  @ViewBuilder private func personalPlaces(_ personal: DemoPlaceLibrary.Matches) -> some View {
    if !personal.saved.isEmpty {
      Section("Gespeichert") {
        ForEach(personal.saved) { entry in personalRow(entry, saved: true) }
      }
    }
    if !personal.recent.isEmpty {
      Section("Zuletzt gewählt") {
        ForEach(personal.recent) { entry in personalRow(entry, saved: false) }
      }
    }
    if model.engine != nil,
      (personal.saved.contains(where: { !model.canSelectPlace($0.place, target: target) })
        || personal.recent.contains(where: { !model.canSelectPlace($0.place, target: target) }))
    {
      Section {
        Button("Andere Offline-Karte öffnen", systemImage: "map") {
          dismiss()
          showRegions()
        }.frame(minHeight: 44)
      } footer: {
        Text(
          "Einige deiner Orte liegen außerhalb der geöffneten Region. Öffne dafür eine passende Offline-Karte."
        )
        .font(.caption).fontWeight(.regular).foregroundStyle(.secondary)
        .lineLimit(nil).fixedSize(horizontal: false, vertical: true)
      }
    }
  }

  private func personalRow(_ entry: DemoPlaceLibrary.Entry, saved: Bool) -> some View {
    let available = model.canSelectPlace(entry.place, target: target)
    return HStack(spacing: 8) {
      Button {
        guard !model.busy, model.canSelectPlace(entry.place, target: target) else { return }
        focused = false
        selected(entry.place)
      } label: {
        HStack(spacing: 12) {
          Image(systemName: saved ? "star.fill" : "clock").foregroundStyle(.secondary)
            .accessibilityHidden(true)
          VStack(alignment: .leading, spacing: 4) {
            Text(entry.place.name).foregroundStyle(.primary)
            Text(
              available
                ? placeSubtitle(entry.place)
                : model.engine == nil ? "Offline-Karte öffnen" : "Außerhalb dieser Offline-Region"
            )
            .font(.caption).foregroundStyle(.secondary)
          }.frame(maxWidth: .infinity, alignment: .leading)
        }.frame(minHeight: 44).contentShape(Rectangle())
      }.buttonStyle(.plain).disabled(model.busy || !available)
        .accessibilityHint(selectionHint)
      Menu {
        if saved {
          Button("Umbenennen", systemImage: "pencil") {
            editedName = entry.place.name
            renaming = entry
          }
          Button("Aus gespeicherten Orten entfernen", systemImage: "star.slash", role: .destructive)
          {
            model.places.removeSaved(id: entry.id)
          }
        } else {
          Button("Ort speichern", systemImage: "star") { model.places.save(entry.place) }
          Button("Aus letzten Auswahlen entfernen", systemImage: "trash", role: .destructive) {
            model.places.removeRecent(id: entry.id)
          }
        }
      } label: {
        Image(systemName: "ellipsis.circle").frame(width: 44, height: 44)
      }.menuStyle(.borderlessButton).menuIndicator(.hidden)
        .accessibilityLabel("Aktionen für \(entry.place.name)")
    }
  }

  private func placeSubtitle(_ place: DemoPlace) -> String {
    if place.category == nil && place.kind == nil {
      return DemoPlaceText.coordinates(place.coordinate)
    }
    return DemoPlaceText.category(place.category, kind: place.kind)
  }

  private var selectionHint: String {
    target == .start
      ? "Als Start übernehmen"
      : target == .destination ? "Als Ziel übernehmen" : "Auf der Karte anzeigen"
  }

  @ViewBuilder private func searchStatus(_ personal: DemoPlaceLibrary.Matches) -> some View {
    if model.engine == nil {
      VStack(alignment: .leading, spacing: 8) {
        Text("Öffne eine Offline-Karte, um auch nach neuen Orten zu suchen.")
          .font(.callout).foregroundStyle(.secondary)
        Button("Offline-Karte öffnen", systemImage: "map") {
          dismiss()
          showRegions()
        }.frame(minHeight: 44)
      }
    } else if model.searching {
      VStack(spacing: 12) {
        ProgressView()
        Text("Offline-Region wird durchsucht …").font(.callout).foregroundStyle(.secondary)
      }.frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityElement(children: .combine)
    } else if let error = model.searchError {
      ContentUnavailableView {
        Label("Suche nicht möglich", systemImage: "exclamationmark.triangle")
      } description: {
        Text(error)
      } actions: {
        Button("Erneut suchen", systemImage: "arrow.clockwise") { model.retrySearch() }
          .buttonStyle(.bordered)
      }
    } else if model.results.isEmpty {
      if !personal.saved.isEmpty || !personal.recent.isEmpty {
        Text("Keine weiteren Kartentreffer in dieser Region.")
          .font(.caption).foregroundStyle(.secondary)
      } else {
        ContentUnavailableView {
          Label("Keine passenden Orte", systemImage: "magnifyingglass")
        } description: {
          Text(
            "Für „\(trimmedQuery)“ gibt es in dieser Offline-Region keine Kartentreffer. Versuche eine andere Schreibweise oder öffne die Karte der gesuchten Region."
          )
        } actions: {
          VStack(spacing: 8) {
            Button("Suchbegriff ändern", systemImage: "pencil") { focused = true }
              .frame(minHeight: 44).buttonStyle(.borderedProminent)
            Button("Andere Offline-Karte öffnen", systemImage: "map") {
              dismiss()
              showRegions()
            }.frame(minHeight: 44).buttonStyle(.bordered)
          }
        }
      }
    }
  }

  private var searchResults: some View {
    Section {
      ForEach(model.results, id: \.identity) { result in
        Button {
          focused = false
          selected(DemoPlace(result))
        } label: {
          HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
              Text(result.label).foregroundStyle(.primary)
              Text(DemoPlaceText.category(result.category, kind: result.kind))
                .font(.caption).foregroundStyle(.secondary)
            }.frame(maxWidth: .infinity, alignment: .leading)
            Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
              .accessibilityHidden(true)
          }.padding(.vertical, 4).frame(minHeight: 44).contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(model.busy)
        .accessibilityHint(selectionHint)
      }
    } header: {
      Text(
        model.results.count == 1
          ? "1 Treffer in der Offline-Region" : "\(model.results.count) angezeigte Treffer")
    } footer: {
      if model.results.count >= 20 {
        Text(
          "Die Suche zeigt bis zu 20 Treffer. Ergänze zum Beispiel einen Orts- oder Straßennamen, um die Auswahl einzugrenzen."
        )
        .font(.caption).fontWeight(.regular).foregroundStyle(.secondary)
        .lineLimit(nil).fixedSize(horizontal: false, vertical: true)
      }
    }
  }

  private func suggestedSearch(_ query: String, image: String) -> some View {
    Button {
      model.query = query
      focused = false
    } label: {
      Label(query, systemImage: image)
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
    }.accessibilityHint("In der geöffneten Offline-Region suchen")
  }
}

struct DemoSearchSuggestion: Identifiable, Equatable {
  let query: String
  let symbol: String
  var id: String { query }

  static func forPreset(_ preset: POIImportPreset?) -> [Self] {
    let values: [(String, String)]
    switch preset {
    case .firefighting:
      values = [("Hydranten", "drop.fill"), ("Löschwasser", "drop.circle.fill"),
        ("Feuerwehr", "flame.fill"), ("Rettungspunkte", "mappin.and.ellipse")]
    case .outdoor:
      values = [("Trinkwasser", "drop.fill"), ("Gipfel", "mountain.2.fill"),
        ("Schutzhütten", "house.fill"), ("Camping", "tent.fill")]
    case .cycling:
      values = [("Reparaturstation", "wrench.fill"), ("Trinkwasser", "drop.fill"),
        ("Fahrradverleih", "bicycle"), ("Rastplätze", "leaf.fill")]
    case .travel:
      values = [("Hotels", "bed.double.fill"), ("Sehenswürdigkeiten", "binoculars.fill"),
        ("Bahnhof", "tram.fill"), ("Parkplatz", "parkingsign")]
    default:
      values = [("Café", "cup.and.saucer"), ("Supermarkt", "cart"),
        ("Apotheke", "cross.case"), ("Tankstelle", "fuelpump")]
    }
    return values.map { Self(query: $0.0, symbol: $0.1) }
  }
}
