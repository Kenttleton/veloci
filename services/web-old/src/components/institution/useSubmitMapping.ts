import { useCreateInstitution, useUpdateInstitution } from '../../api/generated/velociAPI'
import type { InstitutionView } from '../../api/generated/velociAPI.schemas'
import { mappingValuesToRequestBody, type MappingFormValues } from './mappingForm'

interface SubmitMappingResult {
  institutionId: string
  /** True if a new institution was created (name was new/changed); false if the starting institution was updated in place. */
  forked: boolean
}

/**
 * Shared fork-or-update mechanic: if the submitted name matches the institution this
 * editor started from, updates it in place (affecting every account sharing it).
 * Otherwise (no starting institution, or the name changed) creates a new institution.
 */
export function useSubmitMapping() {
  const createInstitutionMutation = useCreateInstitution()
  const updateInstitutionMutation = useUpdateInstitution()

  async function submitMapping(
    startingInstitution: InstitutionView | null,
    values: MappingFormValues,
  ): Promise<SubmitMappingResult> {
    const body = mappingValuesToRequestBody(values)

    if (startingInstitution && startingInstitution.institution_name === body.institution_name) {
      const res = await updateInstitutionMutation.mutateAsync({ id: startingInstitution.id, data: body })
      return { institutionId: res.data.data.id, forked: false }
    }

    const res = await createInstitutionMutation.mutateAsync({ data: body })
    return { institutionId: res.data.data.id, forked: true }
  }

  return {
    submitMapping,
    pending: createInstitutionMutation.isPending || updateInstitutionMutation.isPending,
  }
}
