import { zodResolver } from "@hookform/resolvers/zod";
import { type FieldPath, type UseFormRegisterReturn, useForm } from "react-hook-form";
import { EMPTY_FORM, type Form } from "@/lib/manifest";
import { formSchema } from "./form-schema";

export type FieldFn = (name: FieldPath<Form>) => UseFormRegisterReturn;
export type SetFn = (patch: Partial<Form>) => void;

// useWizardForm holds the wizard's answers in react-hook-form over the zod
// schema. Nothing validates on its own before a step's primary is clicked;
// after that a field with an error re-checks on every change, so the
// message leaves as soon as the answer is fixed, and a typed value is
// checked when the field loses focus.
export function useWizardForm() {
  const form = useForm<Form>({ defaultValues: EMPTY_FORM, resolver: zodResolver(formSchema) });
  const f = form.watch();

  const field: FieldFn = (name) =>
    form.register(name, {
      onChange: () => { if (form.getFieldState(name).error) void form.trigger(name); },
      onBlur: () => { if (String(form.getValues(name) ?? "").trim()) void form.trigger(name); },
    });

  // set writes the answers a card or a picker gave. A new card can make a
  // shown error stale, so the fields that show one are checked again.
  const set: SetFn = (patch) => {
    for (const [k, v] of Object.entries(patch)) form.setValue(k as keyof Form, v as never);
    const shown = Object.keys(form.formState.errors) as FieldPath<Form>[];
    if (shown.length) void form.trigger(shown);
  };

  return { form, f, field, set };
}
