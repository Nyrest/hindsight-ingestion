import { SchemaField } from "@/components/schema-field";
import type { FieldSpec } from "@/lib/types";

export function GitSourceFields({ fields, config, onChange, errors, editing }: {
  fields: FieldSpec[]; config: Record<string, unknown>; onChange: (key: string, value: unknown) => void;
  errors: Record<string, string>; editing: boolean;
}) {
  const refType = String(config.refType || "branch");
  return (
    <div className="grid gap-4">
      {fields.filter((field) => field.key === "tagPattern" ? refType === "latest_tag" : field.key !== "ref" || refType !== "latest_tag").map((field) => (
        <SchemaField key={field.key} field={field.key === "ref" ? { ...field, required: refType !== "branch" } : field}
          idPrefix="src" value={config[field.key]} onChange={(value) => onChange(field.key, value)} error={errors[field.key]} editing={editing} />
      ))}
    </div>
  );
}
