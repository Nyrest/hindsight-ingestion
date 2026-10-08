import { useId } from "react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { ProxyConfig } from "@/lib/types";

export function ProxyEditor({
  value,
  onChange,
  mode,
  onModeChange,
  error,
}: {
  value: ProxyConfig;
  onChange: (proxy: ProxyConfig) => void;
  mode?: "global" | "override";
  onModeChange?: (mode: "global" | "override") => void;
  error?: string;
}) {
  const { t } = useTranslation();
  const id = useId();
  const inherited = mode === "global";
  const manual = !inherited && !["default", "none"].includes(value.type);
  return (
    <div className="space-y-4">
      <FieldRow label={t("proxy.title")} htmlFor={`${id}-type`} error={error}>
        <Select
          value={inherited ? "global" : value.type}
          onValueChange={(type) => {
            // The native select can emit an empty value during form reset.
            if (!type) return;
            if (type === "global") {
              onModeChange?.("global");
              return;
            }
            onModeChange?.("override");
            onChange({ ...value, type: type as ProxyConfig["type"] });
          }}
        >
          <SelectTrigger id={`${id}-type`} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {onModeChange && (
              <SelectItem value="global">{t("proxy.global")}</SelectItem>
            )}
            {(["default", "none", "http", "https", "socks5"] as const).map(
              (type) => (
                <SelectItem key={type} value={type}>
                  {t(`proxy.types.${type}`)}
                </SelectItem>
              ),
            )}
          </SelectContent>
        </Select>
      </FieldRow>
      {!inherited && value.type === "default" && (
        <p className="text-sm text-muted-foreground">
          {t("proxy.defaultHelp")}
        </p>
      )}
      {manual && (
        <>
          <FieldRow
            label={t("proxy.address")}
            htmlFor={`${id}-address`}
            required
          >
            <Input
              id={`${id}-address`}
              value={value.address}
              placeholder="proxy.example.com:8080"
              onChange={(e) => onChange({ ...value, address: e.target.value })}
              autoComplete="off"
            />
          </FieldRow>
          <div className="grid gap-4 sm:grid-cols-2">
            <FieldRow label={t("proxy.username")} htmlFor={`${id}-username`}>
              <Input
                id={`${id}-username`}
                value={value.username}
                onChange={(e) =>
                  onChange({ ...value, username: e.target.value })
                }
                autoComplete="off"
              />
            </FieldRow>
            <FieldRow label={t("proxy.password")} htmlFor={`${id}-password`}>
              <Input
                id={`${id}-password`}
                type="password"
                value={value.password}
                onChange={(e) =>
                  onChange({ ...value, password: e.target.value })
                }
                autoComplete="new-password"
              />
            </FieldRow>
          </div>
        </>
      )}
    </div>
  );
}
