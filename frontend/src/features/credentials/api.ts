import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { qk } from "@/lib/query";
import type { CredentialCreate, CredentialPatch } from "@/lib/types";

export function useCredentials() {
  return useQuery({ queryKey: qk.credentials, queryFn: api.listCredentials });
}

function useInvalidateCredentials() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: qk.credentials });
    void qc.invalidateQueries({ queryKey: qk.dashboard });
  };
}

export function useCreateCredential() {
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (body: CredentialCreate) => api.createCredential(body),
    onSuccess: invalidate,
  });
}

export function useUpdateCredential() {
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CredentialPatch }) => api.updateCredential(id, body),
    onSuccess: invalidate,
  });
}

export function useDeleteCredential() {
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (id: string) => api.deleteCredential(id),
    onSuccess: invalidate,
  });
}

export function useTestCredential() {
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (id: string) => api.testCredential(id),
    onSettled: invalidate,
  });
}

export function useRefreshCredential() {
  const invalidate = useInvalidateCredentials();
  return useMutation({
    mutationFn: (id: string) => api.refreshCredential(id),
    onSettled: invalidate,
  });
}

/** Starts the OAuth flow and navigates the browser to the provider. */
export function useOAuthStart() {
  return useMutation({
    mutationFn: (id: string) => api.oauthStart(id),
    onSuccess: (res) => {
      window.location.href = res.authUrl;
    },
  });
}

export function useStrategies(credentialId: string, bankId: string) {
  return useQuery({
    queryKey: qk.strategies(credentialId, bankId),
    queryFn: () => api.strategies(credentialId, bankId),
    enabled: !!credentialId && !!bankId,
    staleTime: 60_000,
    retry: false,
  });
}

export function useBrowse(
  credentialId: string,
  parentId: string,
  kind: string,
  enabled = true,
  config?: Record<string, unknown>,
) {
  return useQuery({
    queryKey: [...qk.browse(credentialId, parentId, kind), config ?? null],
    queryFn: () => api.browse(credentialId, { parentId, kind, config }),
    enabled: enabled && !!credentialId,
    staleTime: 30_000,
    retry: false,
  });
}
