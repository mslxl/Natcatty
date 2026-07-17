import type { JsonValue } from "./generated/plugin-contract.js";

function assertJsonValueInternal(value: unknown, ancestors: WeakSet<object>): void {
  if (value === null || typeof value === "string" || typeof value === "boolean") return;
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new TypeError("JSON numbers must be finite");
    return;
  }
  if (typeof value !== "object") {
    throw new TypeError(`Unsupported JSON value type: ${typeof value}`);
  }
  if (ancestors.has(value)) throw new TypeError("JSON values must not contain cycles");
  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      const keys = Object.keys(value);
      const ownKeys = Reflect.ownKeys(value);
      if (keys.length !== value.length || ownKeys.length !== value.length + 1) {
        throw new TypeError("JSON arrays must be dense and contain no named properties");
      }
      for (let index = 0; index < value.length; index += 1) {
        const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
        if (!descriptor || !("value" in descriptor) || !descriptor.enumerable) {
          throw new TypeError("JSON arrays must contain enumerable data properties only");
        }
        assertJsonValueInternal(descriptor.value, ancestors);
      }
      return;
    }
    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) {
      throw new TypeError("JSON objects must be plain records");
    }
    const stringKeys = Object.keys(value);
    const ownKeys = Reflect.ownKeys(value);
    if (ownKeys.length !== stringKeys.length) {
      throw new TypeError("JSON objects must not contain symbols or non-enumerable properties");
    }
    for (const key of stringKeys) {
      const descriptor = Object.getOwnPropertyDescriptor(value, key);
      if (!descriptor || !("value" in descriptor)) {
        throw new TypeError("JSON objects must not contain accessor properties");
      }
      assertJsonValueInternal(descriptor.value, ancestors);
    }
  } finally {
    ancestors.delete(value);
  }
}

export function assertJsonValue(value: unknown): asserts value is JsonValue {
  assertJsonValueInternal(value, new WeakSet());
}

function serializeValidatedJsonValue(value: JsonValue): string {
  if (value === null || typeof value !== "object") {
    const serialized = JSON.stringify(value);
    if (serialized === undefined) throw new TypeError("Value is not serializable JSON");
    return serialized;
  }
  if (Array.isArray(value)) {
    const serializedItems: string[] = [];
    for (let index = 0; index < value.length; index += 1) {
      const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
      if (!descriptor || !("value" in descriptor)) {
        throw new TypeError("JSON arrays must contain data properties only");
      }
      serializedItems.push(serializeValidatedJsonValue(descriptor.value as JsonValue));
    }
    return `[${serializedItems.join(",")}]`;
  }
  const serializedEntries: string[] = [];
  for (const key of Object.keys(value)) {
    const descriptor = Object.getOwnPropertyDescriptor(value, key);
    if (!descriptor || !("value" in descriptor)) {
      throw new TypeError("JSON objects must contain data properties only");
    }
    serializedEntries.push(
      `${JSON.stringify(key)}:${serializeValidatedJsonValue(descriptor.value as JsonValue)}`,
    );
  }
  return `{${serializedEntries.join(",")}}`;
}

export function serializeJsonValue(value: unknown): string {
  assertJsonValue(value);
  return serializeValidatedJsonValue(value);
}
