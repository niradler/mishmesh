import type { Endpoint, EndpointPolicy } from "@/api/types";

export function mergePolicy(policy: EndpointPolicy | null | undefined, changes: Partial<EndpointPolicy>): EndpointPolicy {
  return { ...(policy ?? {}), ...changes };
}

export function endpointHostname(endpoint: Pick<Endpoint, "domain" | "public_url" | "subdomain">): string {
  if (endpoint.domain) return endpoint.domain;
  if (endpoint.public_url) {
    try {
      return new URL(endpoint.public_url).host;
    } catch {
      return endpoint.public_url;
    }
  }
  return endpoint.subdomain ?? "";
}

export function supportsPublicAccess(endpoint: Pick<Endpoint, "kind">): boolean {
  return endpoint.kind === "http";
}

export function isEphemeral(endpoint: Pick<Endpoint, "lifecycle">): boolean {
  return endpoint.lifecycle === "ephemeral";
}

export function newEndpointLink(params: {
  domain?: string;
  target?: string;
  serviceId?: string;
  method?: "native" | "proxy";
  publicAccess?: boolean;
}): string {
  const search = new URLSearchParams({ new: "1" });
  if (params.method) search.set("method", params.method);
  if (params.domain) search.set("domain", params.domain);
  if (params.target) search.set("target", params.target);
  if (params.serviceId) search.set("service_id", params.serviceId);
  if (params.publicAccess) search.set("public_access", "cloudflare");
  return `/endpoints?${search.toString()}`;
}
