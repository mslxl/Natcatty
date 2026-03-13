import { useEffect, useState } from "react";
import { localStorageAdapter } from "../../infrastructure/persistence/localStorageAdapter";

/**
 * Hook for persisting a string value to localStorage.
 * @param storageKey - The key to use for localStorage
 * @param fallback - The default value if no stored value exists
 * @param validate - Optional function to validate stored value; returns fallback if invalid
 * @returns A tuple of [value, setValue] similar to useState
 */
export const useStoredString = <T extends string = string>(
    storageKey: string,
    fallback: T,
    validate?: (value: string) => value is T,
) => {
    const [value, setValue] = useState<T>(() => {
        const stored = localStorageAdapter.readString(storageKey);
        if (stored === null) return fallback;
        if (validate) return validate(stored) ? stored : fallback;
        return stored as T;
    });

    useEffect(() => {
        localStorageAdapter.writeString(storageKey, value);
    }, [storageKey, value]);

    return [value, setValue] as const;
};
