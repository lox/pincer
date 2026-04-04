@testable import Pincer

final class InMemoryDefaultsStore: DefaultsStoring {
    var storage: [String: String] = [:]

    func string(forKey defaultName: String) -> String? {
        storage[defaultName]
    }

    func set(_ value: Any?, forKey defaultName: String) {
        storage[defaultName] = value as? String
    }

    func removeObject(forKey defaultName: String) {
        storage.removeValue(forKey: defaultName)
    }
}

final class InMemorySecureStore: SecureStringStoring {
    var storage: [String: String] = [:]

    func string(forKey key: String) -> String? {
        storage[key]
    }

    func setString(_ value: String, forKey key: String) {
        storage[key] = value
    }

    func removeValue(forKey key: String) {
        storage.removeValue(forKey: key)
    }
}
