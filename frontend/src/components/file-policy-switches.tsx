import { AudioLines, FileText, FileType2, Image } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import type { FilePolicy } from "@/lib/types";
import { cn } from "@/lib/utils";

const GROUPS: Array<{ key: keyof FilePolicy; icon: typeof FileText }> = [
  { key: "plainText", icon: FileText },
  { key: "documents", icon: FileType2 },
  { key: "images", icon: Image },
  { key: "audios", icon: AudioLines },
];

export const DEFAULT_FILE_POLICY: FilePolicy = { plainText: true, documents: true, images: false, audios: false };

export function FilePolicySwitches({
  value,
  onChange,
  disabled,
  idPrefix = "fp",
  groups,
}: {
  value: FilePolicy;
  onChange: (value: FilePolicy) => void;
  disabled?: boolean;
  idPrefix?: string;
  groups?: Array<keyof FilePolicy>;
}) {
  const { t } = useTranslation();
  return (
    <div className={cn("grid gap-2 sm:grid-cols-2", disabled && "opacity-60")}>
      {GROUPS.filter((group) => !groups || groups.includes(group.key)).map(({ key, icon: Icon }) => (
        <label
          key={key}
          htmlFor={`${idPrefix}-${key}`}
          className="flex cursor-pointer items-center gap-3 rounded-md border px-3 py-2.5 transition-colors hover:bg-accent/40"
        >
          <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
            <Icon className="size-4" />
          </span>
          <span className="min-w-0 flex-1 text-sm font-medium">{t(`filePolicy.${key}`)}</span>
          <Switch
            id={`${idPrefix}-${key}`}
            checked={!!value[key]}
            disabled={disabled}
            onCheckedChange={(c) => onChange({ ...value, [key]: c })}
          />
        </label>
      ))}
    </div>
  );
}
