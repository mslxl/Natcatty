/**
 * Persist plugin sync secrets then connect; roll back just-created secrets if
 * connect (or a later put) fails so bad passwords are not left in OS storage.
 */

export type PluginSyncSecretRef = { kind: 'secret'; id: string; key: string };

export interface PluginSyncConnectSecretInput {
  secretKey: string;
  value: string;
}

export interface StorePluginSyncSecretsThenConnectParams {
  providerId: string;
  secrets: readonly PluginSyncConnectSecretInput[];
  /** Reused when `secrets` is empty (edit non-secret fields / reconnect). */
  existingCredential?: PluginSyncSecretRef;
  putSecret: (params: {
    providerId: string;
    key: string;
    value: string;
  }) => Promise<PluginSyncSecretRef>;
  deleteSecrets: (params: {
    providerId: string;
    keys: string[];
  }) => Promise<unknown>;
  connect: (credential: PluginSyncSecretRef | undefined) => Promise<void>;
}

export async function storePluginSyncSecretsThenConnect(
  params: StorePluginSyncSecretsThenConnectParams,
): Promise<void> {
  const createdKeys: string[] = [];
  let credential: PluginSyncSecretRef | undefined = params.existingCredential;

  try {
    if (params.secrets.length > 0) {
      credential = undefined;
      for (const secret of params.secrets) {
        const ref = await params.putSecret({
          providerId: params.providerId,
          key: secret.secretKey,
          value: secret.value,
        });
        createdKeys.push(secret.secretKey);
        // SyncConnectPayload.credential carries the primary (first) secret;
        // additional secrets remain addressable via secrets.get(key).
        if (!credential) credential = ref;
      }
    }
    await params.connect(credential);
  } catch (error) {
    if (createdKeys.length > 0) {
      try {
        await params.deleteSecrets({
          providerId: params.providerId,
          keys: [...createdKeys],
        });
      } catch {
        /* best-effort; surface the original connect/put error */
      }
    }
    throw error;
  }
}
