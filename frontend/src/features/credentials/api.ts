import { infiniteQueryOptions, useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { qk } from "@/lib/query";
import type { BrowseRequest, BrowseResult, CredentialCreate, CredentialPatch } from "@/lib/types";

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
  request: BrowseRequest,
  enabled = true,
) {
  return useQuery({
    queryKey: qk.browse(credentialId, request),
    queryFn: ({ signal }) => api.browse(credentialId, request, signal),
    enabled: enabled && !!credentialId,
    staleTime: 30_000,
    retry: false,
  });
}

function mergeBrowsePages(data: InfiniteData<BrowseResult>): BrowseResult {
  return {
    ...data.pages[0],
    nextCursor: data.pages.at(-1)?.nextCursor,
    items: [...new Map(data.pages.flatMap((page) => page.items).map((item) => [item.id, item])).values()],
  };
}

export function infiniteBrowseOptions(credentialId: string, request: BrowseRequest) {
  return infiniteQueryOptions({
    queryKey: [...qk.browse(credentialId, request), "infinite"],
    queryFn: ({ pageParam, signal }) => api.browse(credentialId, { ...request, cursor: pageParam }, signal),
    initialPageParam: "",
    getNextPageParam: (lastPage, _pages, _lastParam, params) => {
      const cursor = lastPage.nextCursor;
      return cursor && !params.includes(cursor) ? cursor : undefined;
    },
    select: mergeBrowsePages,
    // Keep browsing explicit: reopening must not refetch every cached batch.
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: false,
  });
}

export function useInfiniteBrowse(credentialId: string, request: BrowseRequest, enabled = true) {
  const qc = useQueryClient();
  const options = infiniteBrowseOptions(credentialId, request);
  const query = useInfiniteQuery({ ...options, enabled: enabled && !!credentialId });
  return {
    ...query,
    // Refresh starts at the first batch instead of fetching every previously loaded batch.
    refresh: () => qc.resetQueries({ queryKey: options.queryKey, exact: true }),
  };
}
