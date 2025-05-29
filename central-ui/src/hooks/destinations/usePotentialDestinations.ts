import { useEffect, useMemo } from 'react';
import { useProxy } from '../proxy';
import { GET_POTENTIAL_DESTINATIONS } from '@/graphql';
import { deepClone, safeJsonParse } from '@odigos/ui-kit/functions';
import { useDestinationCategories } from './useDestinationCategories';
import { type ISetupState, useSetupStore } from '@odigos/ui-kit/store';

interface PotentialDestination {
  type: string;
  fields: string;
}

interface GetPotentialDestinationsData {
  potentialDestinations: PotentialDestination[];
}

const checkIfConfigured = (
  configuredDest: ISetupState['configuredDestinations'][0],
  potentialDest: PotentialDestination,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  autoFilledFields: Record<string, any>,
) => {
  const typesMatch = configuredDest.type === potentialDest.type;
  if (!typesMatch) return false;

  let fieldsMatch = false;

  for (const { key, value } of configuredDest.form.fields) {
    if (Object.hasOwn(autoFilledFields, key)) {
      // An exact match is when all "ifs" result in true.
      // If one resulted with false, it is not an exact match and therefore not an "already configured destination".
      if (autoFilledFields[key] === value) {
        fieldsMatch = true;
      } else {
        fieldsMatch = false;
        break;
      }
    }
  }

  return fieldsMatch;
};

export const usePotentialDestinations = () => {
  const { configuredDestinations } = useSetupStore();
  const { categories } = useDestinationCategories();

  const { fetch, loading, data } = useProxy<GetPotentialDestinationsData>(GET_POTENTIAL_DESTINATIONS);

  const potentialDestinations = useMemo(() => {
    if (!categories || !data?.potentialDestinations) return [];

    // Create a deep copy of destination types to manipulate
    const parsed: typeof categories = deepClone(categories);

    // Map over the potential destinations
    return data.potentialDestinations
      .map((pd) => {
        for (const category of parsed) {
          const autoFilledFields = safeJsonParse<{ [key: string]: string }>(pd.fields, {});
          const alreadyConfigured = configuredDestinations.find((cd) => checkIfConfigured(cd, pd, autoFilledFields));

          if (!alreadyConfigured) {
            const idx = category.items.findIndex((item) => item.type === pd.type);

            if (idx !== -1) {
              return {
                // Spread the matched destination type data into the potential destination
                ...category.items[idx],
                fields: category.items[idx].fields.map((field) => ({
                  ...field,
                  initialValue: autoFilledFields[field.name],
                })),
              };
            }
          }
        }

        return null;
      })
      .filter((pd) => pd);
  }, [configuredDestinations, categories, data?.potentialDestinations]);

  useEffect(() => {
    if (!data?.potentialDestinations) fetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    loading,
    potentialDestinations,
  };
};
